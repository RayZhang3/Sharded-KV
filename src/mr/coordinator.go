package mr

import (
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sync"
	"time"
)

type Coordinator struct {
	mu         sync.Mutex
	nReduce    int
	nMap       int
	Stage      int
	Mapfiles   []string
	MapTask    []Task
	ReduceTask []Task
}

type Task struct {
	TaskId    int
	FileName  string
	StartTime time.Time
	Status    int
}

// create the enum type
const (
	STAGE_ASSIGNMAP = 0
	STAGE_MAPPING   = 1
	STAGE_REDUCING  = 2
	STAGE_FINISHED  = 3
)

const (
	TASK_IDLE    = 0
	TASK_WORKING = 1
	TASK_DONE    = 2
)

func (c *Coordinator) init(files []string, nReduce int) {

}

func (c *Coordinator) assignTask(reply *Taskinfo) {
	//fmt.Println("assign task acquire Lock")
	c.mu.Lock()
	defer c.mu.Unlock()
	var task *Task
	var found bool
	if c.Stage == STAGE_MAPPING {
		timeOutTask(c.MapTask)
		task, found = findIdleTask(c.MapTask)
		complete := finduncompletedTask(c.MapTask)
		//fmt.Println("Task is %v\n\n", complete)
		if complete {
			c.Stage = STAGE_REDUCING
			reply.RPCtype = RPC_FAIL
			return
		}
	} else if c.Stage == STAGE_REDUCING {
		timeOutTask(c.ReduceTask)
		task, found = findIdleTask(c.ReduceTask)
		complete := finduncompletedTask(c.ReduceTask)
		if complete {
			c.Stage = STAGE_FINISHED
			reply.RPCtype = RPC_FAIL
			return
		}
	} else if c.Stage == STAGE_FINISHED {
		reply.RPCtype = RPC_ALLDONE
		return
	}
	// task != nil
	/*
		if task == nil {
			fmt.Println("task is null")
		}*/
	if !found {
		reply.RPCtype = RPC_FAIL
		return
	}
	if c.Stage == STAGE_MAPPING {
		reply.RPCtype = RPC_MAP
	} else if c.Stage == STAGE_REDUCING {
		reply.RPCtype = RPC_REDUCE
	}
	//fmt.Println("target task", task)
	task.Status = TASK_WORKING
	task.StartTime = time.Now()
	reply.StartTime = task.StartTime
	reply.TaskId = task.TaskId
	reply.Filename = task.FileName
	reply.NMap = c.nMap
	reply.NReduce = c.nReduce
	//fmt.Println("assign task release Lock")
}

func findIdleTask(tasks []Task) (*Task, bool) {
	for idx, t := range tasks {
		//fmt.Println(t)
		if t.Status == TASK_IDLE {
			return &tasks[idx], true
		}
	}
	return nil, false
}

func finduncompletedTask(tasks []Task) bool {
	for _, t := range tasks {
		if t.Status != TASK_DONE {
			return false
		}
	}
	return true
}

func timeOutTask(tasks []Task) {
	var task *Task
	for idx, t := range tasks {
		if t.Status == TASK_WORKING && time.Now().Sub(t.StartTime) > 10e9 {
			task = &tasks[idx]
			task.Status = TASK_IDLE
		}
	}
}

//
func (c *Coordinator) reassignTask() {
	tick := time.Tick(1e9)
	for {
		select {
		case <-tick:
			//fmt.Println("reassign acquire Lock")
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.Stage == STAGE_MAPPING {
				var task *Task
				for idx, t := range c.MapTask {
					if t.Status == TASK_WORKING && time.Now().Sub(t.StartTime) > 10e9 {
						task = &c.MapTask[idx]
						task.Status = TASK_IDLE
					}
				}
			} else if c.Stage == STAGE_REDUCING {
				var task *Task
				for idx, t := range c.ReduceTask {
					if t.Status == TASK_WORKING && time.Now().Sub(t.StartTime) > 10e9 {
						task = &c.ReduceTask[idx]
						task.Status = TASK_IDLE
					}
				}
			}
			complete := finduncompletedTask(c.MapTask)
			if c.Stage == STAGE_MAPPING && !complete {
				c.Stage = STAGE_REDUCING
			}
			complete = finduncompletedTask(c.ReduceTask)
			if c.Stage == STAGE_REDUCING && !complete {
				c.Stage = STAGE_FINISHED
			}
			//fmt.Println("reassign release Lock")
		default:
			time.Sleep(5e7)
		}
	}
}

func (c *Coordinator) checkTask(args *Taskinfo, reply *Taskinfo) {
	switch c.Stage {
	case STAGE_MAPPING:
		//fmt.Println("check Task acquire lock")
		c.mu.Lock()
		defer c.mu.Unlock()
		task := &c.MapTask[args.TaskId]
		if task.Status == TASK_WORKING {
			//if task.Status == TASK_WORKING && task.StartTime.Equal(args.StartTime) {
			task.Status = TASK_DONE
		}
		reply.RPCtype = RPC_WORKDONE
	case STAGE_REDUCING:
		c.mu.Lock()
		//fmt.Println("check Task acquire lock")
		defer c.mu.Unlock()
		task := &c.ReduceTask[args.TaskId]
		if task.Status == TASK_WORKING {
			//if task.Status == TASK_WORKING && task.StartTime.Equal(args.StartTime) {
			task.Status = TASK_DONE
		}
		reply.RPCtype = RPC_WORKDONE
	}
	//fmt.Println("check Task release lock")
}

// Your code here -- RPC handlers for the worker to call.
func (c *Coordinator) Handlers(args *Taskinfo, reply *Taskinfo) error {
	//fmt.Printf("master receive request %v\n", args.RPCtype)
	switch args.RPCtype {
	case RPC_REQUEST:
		c.assignTask(reply)
	case RPC_WORKDONE:
		c.checkTask(args, reply)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	ret := false
	if c.Stage == STAGE_FINISHED {
		ret = true
	}
	return ret
}

//
// create a Coordinator.
// main/mrcoordinator.go calls this function.
// nReduce is the number of reduce tasks to use.
//
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{}
	c.Mapfiles = files
	c.nReduce = nReduce
	c.nMap = len(files)
	c.Mapfiles = files
	c.MapTask = make([]Task, c.nMap)
	c.ReduceTask = make([]Task, c.nReduce)
	c.Stage = STAGE_MAPPING
	//c.init(files, nReduce)
	for i := 0; i < c.nMap; i++ {
		c.MapTask[i].TaskId = i
		c.MapTask[i].FileName = files[i]
		c.MapTask[i].Status = TASK_IDLE
		//fmt.Println(c.MapTask[i])
	}
	for i := 0; i < c.nReduce; i++ {
		c.ReduceTask[i].TaskId = i
		c.ReduceTask[i].Status = TASK_IDLE
		//fmt.Println(c.ReduceTask[i])
	}
	//fmt.Println("init coordinator")
	c.server()
	//go c.reassignTask()
	return &c
}
