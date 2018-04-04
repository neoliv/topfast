# topfast (top replacement for short lived processes)

(Not yet 100% tested but already useful, so use and contribute!)

This tool helps when top is not able to find the origin of a CPU load on your Linux system.  
It relies on netlink/taskstats interface to the kernel to track all processes (even the very short lived ones). For example if there is a script forking grep, perl, awk, expr, basename and so on the CPU load may be high but these processes will stay under the radar of usual sampling tools like top.
topfast will gather data about all processes (even the very short lived ones) and publish a summary of the most CPU intensive commands and their origin.  
Hope this helps.  

Install:  

go get github.com/neoliv/topfast


Example:

Here is an output sample:  
```
```  

With the above output you see that the hog.sh is responsible for a lot of short lived processes and that it could be a good idea to rewrite that script.

See the -h below:  
```
```  

