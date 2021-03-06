/* This is a hagoexit
cked down version of getdelays.c
 * (original in /usr/share/doc/linux-doc/accounting/getdelays.c.gz)
 * The idea is to use netlink to access task stats in two ways:
 * - register for all exit events, thus getting stats about all processes at death time (even the very short lived ones)
 * - ask for a given process current stats. Thus we can sample long lived processes.
 *
 * All the API is described in:
 * https://www.kernel.org/doc/Documentation/accounting/taskstats.txt
 * https://www.kernel.org/doc/Documentation/accounting/taskstats-struct.txt
 * https://www.kernel.org/doc/Documentation/accounting/cgroupstats.txt
 */

#include <stdio.h>
#include <stdlib.h>
#include <errno.h>
#include <unistd.h>
#include <poll.h>
#include <string.h>
#include <fcntl.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <signal.h>

#include <linux/genetlink.h>
#include <linux/taskstats.h>
//#include <linux/cgroupstats.h>


#ifndef NO_GO
/* Go handler for process update stats. */
extern void goUpdateStats (int, int, unsigned long, char *);
/* Go handler for process exit stats. */
extern void goExitStats (int, int, unsigned long, char *);
#endif

/*
 * Generic macros for dealing with netlink sockets. Might be duplicated
 * elsewhere. It is recommended that commercial grade applications use
 * libnl or libnetlink and use the interfaces provided by the library
 */
#define GENLMSG_DATA(glh)	((void *)(NLMSG_DATA(glh) + GENL_HDRLEN))
#define GENLMSG_PAYLOAD(glh)	(NLMSG_PAYLOAD(glh, 0) - GENL_HDRLEN)
#define NLA_DATA(na)		((void *)((char*)(na) + NLA_HDRLEN))
#define NLA_PAYLOAD(len)	(len - NLA_HDRLEN)


static int rcvbufsz;
static char name[100];
static int dbg = 0;		// 1 for debug mode.
static int nl_exit_sd = -1;	// socket receiving exit stats
static int nl_query_sd = -1;	// socket to request/get tgid stats
static __u16 id;
static __u32 mypid;

#define PRINTF(fmt, arg...) {			\
	    if (dbg) {				\
		    printf(fmt, ##arg);		\
	    }					\
	}


char _error_msg[2048];

/* TODO use snprintf + \0 */
#define err(fmt, arg...) \
	do { \
		sprintf(_error_msg, fmt, ##arg);	\
		PRINTF("CError: %s\n", _error_msg);  \
	} while (0)

static char *
error_msg ()
{
  return _error_msg;
}


/* Maximum size of response requested or message sent */
#define MAX_MSG_SIZE	1024
/* Maximum number of cpus expected to be specified in a cpumask */
#define MAX_CPUS	32

#define average_ms(t, c) (t / 1000000ULL / (c ? c : 1))

struct msgtemplate
{
  struct nlmsghdr n;
  struct genlmsghdr g;
  char buf[MAX_MSG_SIZE];
};

static char cpumask[8];		// was [100 + 6 * MAX_CPUS] but here we only use all or a few cpus in mask string (eg: "0"  or "1-4").

static int get_family_id (int sd);


/*
 * Create a raw netlink socket and bind
 */
static int
create_nl_socket (int protocol)
{
  int fd;
  struct sockaddr_nl local;

  fd = socket (AF_NETLINK, SOCK_RAW, protocol);
  if (fd < 0)
    return -1;

  if (rcvbufsz)
    if (setsockopt (fd, SOL_SOCKET, SO_RCVBUF,
		    &rcvbufsz, sizeof (rcvbufsz)) < 0)
      {
	err ("Unable to set socket rcv buf size to %d\n", rcvbufsz);
	goto error;
      }

  memset (&local, 0, sizeof (local));
  local.nl_family = AF_NETLINK;

  if (bind (fd, (struct sockaddr *) &local, sizeof (local)) < 0)
    goto error;

  id = get_family_id (fd);
  if (!id)
    {
      fprintf (stderr, "Error getting family id, errno %d\n", errno);
      goto error;
    }
  PRINTF ("family id %d\n", id);

  return fd;
error:
  close (fd);
  return -1;
}


static int
send_cmd (int sd, __u16 nlmsg_type, __u32 nlmsg_pid,
	  __u8 genl_cmd, __u16 nla_type, void *nla_data, int nla_len)
{
  struct nlattr *na;
  struct sockaddr_nl nladdr;
  int r, buflen;
  char *buf;

  struct msgtemplate msg;

  msg.n.nlmsg_len = NLMSG_LENGTH (GENL_HDRLEN);
  msg.n.nlmsg_type = nlmsg_type;
  msg.n.nlmsg_flags = NLM_F_REQUEST;
  msg.n.nlmsg_seq = 0;
  msg.n.nlmsg_pid = nlmsg_pid;
  msg.g.cmd = genl_cmd;
  msg.g.version = 0x1;
  na = (struct nlattr *) GENLMSG_DATA (&msg);
  na->nla_type = nla_type;
  na->nla_len = nla_len + 1 + NLA_HDRLEN;
  memcpy (NLA_DATA (na), nla_data, nla_len);
  msg.n.nlmsg_len += NLMSG_ALIGN (na->nla_len);

  buf = (char *) &msg;
  buflen = msg.n.nlmsg_len;
  memset (&nladdr, 0, sizeof (nladdr));
  nladdr.nl_family = AF_NETLINK;
  while ((r = sendto (sd, buf, buflen, 0, (struct sockaddr *) &nladdr,
		      sizeof (nladdr))) < buflen)
    {
      if (r > 0)
	{
	  buf += r;
	  buflen -= r;
	}
      else if (errno != EAGAIN)
	return -1;
    }
  return 0;
}


/*
 * Probe the controller in genetlink to find the family id
 * for the TASKSTATS family
 */
static int
get_family_id (int sd)
{
  struct
  {
    struct nlmsghdr n;
    struct genlmsghdr g;
    char buf[256];
  } ans;

  int id = 0, rc;
  struct nlattr *na;
  int rep_len;

  strcpy (name, TASKSTATS_GENL_NAME);
  rc = send_cmd (sd, GENL_ID_CTRL, getpid (), CTRL_CMD_GETFAMILY,
		 CTRL_ATTR_FAMILY_NAME, (void *) name,
		 strlen (TASKSTATS_GENL_NAME) + 1);
  if (rc < 0)
    return 0;			/* sendto() failure? */

  rep_len = recv (sd, &ans, sizeof (ans), 0);
  if (ans.n.nlmsg_type == NLMSG_ERROR ||
      (rep_len < 0) || !NLMSG_OK ((&ans.n), rep_len))
    return 0;

  na = (struct nlattr *) GENLMSG_DATA (&ans);
  na = (struct nlattr *) ((char *) na + NLA_ALIGN (na->nla_len));
  if (na->nla_type == CTRL_ATTR_FAMILY_ID)
    {
      id = *(__u16 *) NLA_DATA (na);
    }
  return id;
}

/*
* Read a stat part of a message.
*/
static void
read_stats (pid_t tid, struct taskstats *t, unsigned int *pid,
	    unsigned int *ppid, unsigned int *uid, unsigned long long *cpu,
	    char **cmd)
{
  // /usr/src/linux-headers-4.4.0-116/include/uapi/linux/taskstats.h
  //    /* Basic Accounting Fields start */
  //    char    ac_comm[TS_COMM_LEN];   /* Command name */
  //    __u8    ac_sched __attribute__((aligned(8)));
  //                                    /* Scheduling discipline */
  //    __u8    ac_pad[3];
  //    __u32   ac_uid __attribute__((aligned(8)));
  //                                    /* User ID */
  //    __u32   ac_gid;                 /* Group ID */
  //    __u32   ac_pid;                 /* Process ID */
  //    __u32   ac_ppid;                /* Parent process ID */
  //    __u32   ac_btime;               /* Begin time [sec since 1970] */
  //    __u64   ac_etime __attribute__((aligned(8)));
  //                                    /* Elapsed time [usec] */
  //    __u64   ac_utime;               /* User CPU time [usec] */
  //    __u64   ac_stime;               /* SYstem CPU time [usec] */
  //    __u64   ac_minflt;              /* Minor Page Fault Count */
  //    __u64   ac_majflt;              /* Major Page Fault Count */
  *pid = t->ac_pid;
  *ppid = t->ac_ppid;
  *uid = t->ac_uid;

  *cpu = t->ac_utime + t->ac_stime;
  *cmd = t->ac_comm;

  PRINTF ("   pid:%d ppid:%d uid:%d cpu:%llu cmd:%s\n", *pid, *ppid, *uid,
	  *cpu, *cmd);

}

/*
* Init the nlstat C module.
*/
static int
init_nlstats ()
{
  mypid = getpid ();
  return 0;
}

/*
 * Init the tgid netlink socket.
 */
static int
init_tgid_stats ()
{
  if ((nl_query_sd = create_nl_socket (NETLINK_GENERIC)) < 0)
    {
      err ("error creating Netlink socket\n");
      return -1;
    }
  return 0;
}


/*
 * Read the pid netlink socket to get stats we previously asked for.
 */
static int
get_pid_stats ()
{
  int rep_len, aggr_len, len2;
  struct nlattr *na;
  int len = 0;
  pid_t rtid = 0;

  struct msgtemplate msg;

  while (1)
    {
      rep_len = recv (nl_query_sd, &msg, sizeof (msg), 0);
      PRINTF ("received %d bytes (on tgid socket)\n", rep_len);

      if (rep_len < 0)
	{
	  fprintf (stderr, "nonfatal reply error: errno %d\n", errno);
	  continue;
	}
      if (msg.n.nlmsg_type == NLMSG_ERROR || !NLMSG_OK ((&msg.n), rep_len))
	{
	  struct nlmsgerr *err = NLMSG_DATA (&msg);
	  err ("fatal reply error %d: %s\n", err->error,
	       strerror (err->error));
	  return -1;
	}

      PRINTF ("nlmsghdr size=%zu, nlmsg_len=%d, rep_len=%d\n",
	      sizeof (struct nlmsghdr), msg.n.nlmsg_len, rep_len);


      rep_len = GENLMSG_PAYLOAD (&msg.n);

      na = (struct nlattr *) GENLMSG_DATA (&msg);
      len = 0;
      while (len < rep_len)
	{
	  len += NLA_ALIGN (na->nla_len);
	  PRINTF ("nla_type:%d\n", na->nla_type);
	  switch (na->nla_type)
	    {
	    case TASKSTATS_TYPE_AGGR_TGID:
	      /* Fall through */
	    case TASKSTATS_TYPE_AGGR_PID:
	      aggr_len = NLA_PAYLOAD (na->nla_len);
	      len2 = 0;
	      /* For nested attributes, na follows */
	      na = (struct nlattr *) NLA_DATA (na);
	      while (len2 < aggr_len)
		{
		  PRINTF ("nested nla_type:%d\n", na->nla_type);
		  switch (na->nla_type)
		    {
		    case TASKSTATS_TYPE_PID:
		      rtid = *(int *) NLA_DATA (na);
		      break;
		    case TASKSTATS_TYPE_TGID:
		      rtid = *(int *) NLA_DATA (na);
		      break;
		    case TASKSTATS_TYPE_STATS:
		      PRINTF ("stats (pid:%d):\n", rtid);
		      {
			unsigned int pid;
			unsigned int ppid;
			unsigned int uid;
			unsigned long long cpu;
			char *cmd;
			read_stats (rtid, (struct taskstats *) NLA_DATA (na),
				    &pid, &ppid, &uid, &cpu, &cmd);
			/* Send stats to Go */
#ifndef NO_GO
			goUpdateStats (pid, ppid, cpu, cmd);
#endif
		      }
		      break;
		    default:
		      fprintf (stderr, "Unknown nested nla_type %d\n",
			       na->nla_type);
		      break;
		    }
		  len2 += NLA_ALIGN (na->nla_len);
		  na = (struct nlattr *) ((char *) na + len2);
		}
	      // TODO bad copy/past? bas indent anyway...
	      break;
	    default:
	      fprintf (stderr, "Unknown nla_type %d\n", na->nla_type);
	    case TASKSTATS_TYPE_NULL:
	      break;
	    }
	  na = (struct nlattr *) (GENLMSG_DATA (&msg) + len);
	}
      return 0;			// TODO loop until nothing more to read?
    }
  return -1;
}


/*
 * Request stats regarding a given pid.
 * Send a request using netlink.
 */
static int
request_pid_stats (__u32 pid)
{
  int rc;
  rc = send_cmd (nl_query_sd, id, mypid, TASKSTATS_CMD_GET,
		 TASKSTATS_CMD_ATTR_PID, &pid, sizeof (__u32));
  PRINTF ("Sent tgid %d, retval %d\n", pid, rc);
  if (rc < 0)
    {
      err ("error sending pid cmd for %d\n", pid);
      return -1;
    }
  get_pid_stats ();
  return 0;
}


/*
 * Use netlink to receive all task exit stats.
 * This funtion will not return unless a fatal error occurs.
 */
static int
get_exit_stats ()
{
  int rc, rep_len, aggr_len, len2;

  struct nlattr *na;
  int len = 0;
  pid_t rtid = 0;
  int count = 0;

  struct msgtemplate msg;

  if ((nl_exit_sd = create_nl_socket (NETLINK_GENERIC)) < 0)
    {
      err ("error creating Netlink socket\n");
      return -1;
    }

  strncpy (cpumask, "0", sizeof (cpumask));
  cpumask[sizeof (cpumask) - 1] = '\0';
  PRINTF ("cpumask %s\n", cpumask);

  mypid = getpid ();
  id = get_family_id (nl_exit_sd);
  if (!id)
    {
      err ("error getting family id, errno %d\n", errno);
      goto err;
    }
  PRINTF ("family id %d\n", id);

  rc = send_cmd (nl_exit_sd, id, mypid, TASKSTATS_CMD_GET,
		 TASKSTATS_CMD_ATTR_REGISTER_CPUMASK,
		 &cpumask, strlen (cpumask) + 1);
  PRINTF ("Sent register cpumask '%s', retval %d\n", cpumask, rc);
  if (rc < 0)
    {
      err ("error sending register cpumask '%s''\n", cpumask);
      goto err;
    }

  while (1)
    {
      rep_len = recv (nl_exit_sd, &msg, sizeof (msg), 0);
      PRINTF ("received %d bytes\n", rep_len);

      if (rep_len < 0)
	{
	  fprintf (stderr, "nonfatal reply error: errno %d\n", errno);
	  continue;
	}
      if (msg.n.nlmsg_type == NLMSG_ERROR || !NLMSG_OK ((&msg.n), rep_len))
	{
	  struct nlmsgerr *err = NLMSG_DATA (&msg);
	  err ("fatal reply error %d: %s\n", err->error,
	       strerror (err->error));
	  goto done;
	}

      PRINTF ("nlmsghdr size=%zu, nlmsg_len=%d, rep_len=%d\n",
	      sizeof (struct nlmsghdr), msg.n.nlmsg_len, rep_len);


      rep_len = GENLMSG_PAYLOAD (&msg.n);


      na = (struct nlattr *) GENLMSG_DATA (&msg);
      len = 0;
      while (len < rep_len)
	{
	  len += NLA_ALIGN (na->nla_len);
	  switch (na->nla_type)
	    {
	    case TASKSTATS_TYPE_AGGR_TGID:
	      /* Fall through */
	    case TASKSTATS_TYPE_AGGR_PID:
	      aggr_len = NLA_PAYLOAD (na->nla_len);
	      len2 = 0;
	      /* For nested attributes, na follows */
	      na = (struct nlattr *) NLA_DATA (na);
	      while (len2 < aggr_len)
		{
		  switch (na->nla_type)
		    {
		    case TASKSTATS_TYPE_PID:
		      rtid = *(int *) NLA_DATA (na);
		      break;
		    case TASKSTATS_TYPE_TGID:
		      rtid = *(int *) NLA_DATA (na);
		      break;
		    case TASKSTATS_TYPE_STATS:
		      count++;
		      PRINTF ("stats (pid:%d count:%d):\n", rtid, count);
		      {
			unsigned int pid;
			unsigned int ppid;
			unsigned int uid;
			unsigned long long cpu;
			char *cmd;
			read_stats (rtid, (struct taskstats *) NLA_DATA (na),
				    &pid, &ppid, &uid, &cpu, &cmd);
			/* Send stats to Go */
#ifndef NO_GO
			goExitStats (pid, ppid, cpu, cmd);
#endif
		      }
		      break;
		    default:
		      fprintf (stderr, "Unknown nested" " nla_type %d\n",
			       na->nla_type);
		      break;
		    }
		  len2 += NLA_ALIGN (na->nla_len);
		  na = (struct nlattr *) ((char *) na + len2);
		}
	      break;

	    default:
	      fprintf (stderr, "Unknown nla_type %d\n", na->nla_type);
	    case TASKSTATS_TYPE_NULL:
	      break;
	    }
	  na = (struct nlattr *) (GENLMSG_DATA (&msg) + len);
	}
    }
done:
  rc = send_cmd (nl_exit_sd, id, mypid, TASKSTATS_CMD_GET,
		 TASKSTATS_CMD_ATTR_DEREGISTER_CPUMASK,
		 &cpumask, strlen (cpumask) + 1);
  PRINTF ("Sent deregister mask, retval %d\n", rc);
err:
  close (nl_exit_sd);
  return -1;
}

#ifdef NO_GO
int
main (int argc, char *argv[])
{
  if (init_nlstats () < 0)
    {
      return -1;
    }
  if (init_tgid_stats () < 0)
    {
      return -1;
    }
  request_pid_stats (1);
  request_pid_stats (13057);
  get_pid_stats ();
  get_pid_stats ();
  PRINTF
    ("-----------------------------------------------------------------------------------\n");
  get_exit_stats ();
  return 0;
}
#endif
