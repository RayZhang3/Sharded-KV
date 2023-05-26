package mr

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"time"
)

type Coordinator struct {
	// Your definitions here.
	nReduce int
}

// Your code here -- RPC handlers for the worker to call.
func (c *Coordinator) Handlers(args *Taskinfo, reply *Taskinfo) error {
	/*
		t := args
		switch t.RPCtype {
			case REQUEST:
		}
		switch
	*/
	var flag bool
	flag = false
	fmt.Printf("master receive request %v\n", args.RPCtype)
	if !flag {
		time.Sleep(1e9)
		/*
			reply.RPCtype = MAP
			reply.NReduce = c.nReduce
		*/

		reply.RPCtype = REDUCE
		reply.NMap = 10
		flag = true
	} else {
		reply.RPCtype = REDUCE
		reply.NMap = 10
	}
	return nil
}

/*
//
// an example RPC handler.
//
// the RPC argument and reply types are defined in rpc.go.
//
func (c *Coordinator) Example(args *ExampleArgs, reply *ExampleReply) error {
	reply.Y = args.X + 1
	return nil
}
*/
//
// start a thread that listens for RPCs from worker.go
//
func (c *Coordinator) server() {
	rpc.Register(c)
	rpc.HandleHTTP()
	//l, e := net.Listen("tcp", ":1234")
	sockname := coordinatorSock()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

//
// main/mrcoordinator.go calls Done() periodically to find out
// if the entire job has finished.
//
func (c *Coordinator) Done() bool {
	ret := false

	// Your code here.
	//ret = true

	return ret
}

//
// create a Coordinator.
// main/mrcoordinator.go calls this function.
// nReduce is the number of reduce tasks to use.
//
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{nReduce: nReduce}

	// Your code here.

	c.server()
	return &c
}
