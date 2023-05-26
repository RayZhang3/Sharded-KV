package mr

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"log"
	"net/rpc"
	"os"
	"sort"
	"time"
)

//
// Map functions return a slice of KeyValue.
//
type KeyValue struct {
	Key   string
	Value string
}

//
// use ihash(key) % NReduce to choose the reduce
// task number for each KeyValue emitted by Map.
//
func ihash(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() & 0x7fffffff)
}

//
// main/mrworker.go calls this function.
//
func Worker(mapf func(string, string) []KeyValue,
	reducef func(string, []string) string) {

	// Your worker implementation here.
	for {
		taskinfo := requestTask()
		fmt.Printf("Send request task and get%v\n", taskinfo)
		switch taskinfo.RPCtype {
		case MAP:
			fmt.Printf("Receive Map task\n")
			execMap(taskinfo, mapf)
			return
		case REDUCE:
			fmt.Printf("Receive Reduce task\n")
			execReduce(taskinfo, reducef)
			return
		case WORKDONE:
			os.Exit(0)
		default:
			time.Sleep(time.Second)
		}
	}

	// uncomment to send the Example RPC to the coordinator.
}

func requestTask() *Taskinfo {
	args := Taskinfo{}
	args.RPCtype = REQUEST
	reply := Taskinfo{}
	call("Coordinator.Handlers", &args, &reply)
	fmt.Printf("Send request %v and get %v\n", args.RPCtype, reply.RPCtype)
	return &reply
}

// for sorting by key.
type ByKey []KeyValue

// for sorting by key.
func (a ByKey) Len() int           { return len(a) }
func (a ByKey) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByKey) Less(i, j int) bool { return a[i].Key < a[j].Key }

func execReduce(t *Taskinfo, reducef func(string, []string) string) {
	taskId := t.TaskId
	taskId = 0
	nMap := t.NMap

	kva := make([]KeyValue, 0)
	for i := 0; i < nMap; i++ {
		iname := fmt.Sprintf("mr-%v-%v", taskId, i)
		fmt.Println("try to open ", iname, "\n")
		ifile, err := os.Open(iname)
		if err != nil {
			panic("open file failed")
		}
		//read such a file back:
		dec := json.NewDecoder(ifile)
		for {
			var kv KeyValue
			if err := dec.Decode(&kv); err != nil {
				break
			}
			kva = append(kva, kv)
		}
	}
	sort.Sort(ByKey(kva))
	oname := fmt.Sprintf("mr-out-%v", taskId)
	ofile, _ := os.Create(oname)

	// call Reduce on each distinct key in intermediate[],
	// and print the result to mr-out-0.
	//

	i := 0
	for i < len(kva) {
		j := i + 1
		for j < len(kva) && kva[j].Key == kva[i].Key {
			j++
		}
		values := []string{}
		for k := i; k < j; k++ {
			values = append(values, kva[k].Value)
		}
		output := reducef(kva[i].Key, values)
		// this is the correct format for each line of Reduce output.
		fmt.Fprintf(ofile, "%v %v\n", kva[i].Key, output)

		i = j
	}

	ofile.Close()
}

func execMap(t *Taskinfo, mapf func(string, string) []KeyValue) {

	// read input file
	// pass it to Map
	// accumulate the intermediate Map output.
	//
	filename := t.Filename
	filename = "pg-grimm.txt"
	nReduce := t.NReduce
	//intermediate := [nReduce][]mr.KeyValue{}
	intermediate := make([][]KeyValue, nReduce)
	for i := 0; i < nReduce; i++ {
		intermediate[i] = make([]KeyValue, 0)
	}

	file, err := os.Open(filename)
	if err != nil {
		log.Fatalf("cannot open %v", filename)
	}
	content, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalf("cannot read %v", filename)
	}
	file.Close()
	kva := mapf(filename, string(content))
	// map function is called once for each file of input,
	// The return value is a slice of key/value pairs.

	for _, i := range kva {
		bucket := ihash(i.Key) % nReduce
		intermediate[bucket] = append(intermediate[bucket], i)
	}

	for i := 0; i < nReduce; i++ {
		sort.Sort(ByKey(intermediate[i]))
	}

	/*
		for i := 0; i < nReduce; i++ {
			for _, v := range intermediate[i] {
				fmt.Println("key and value is ", v)
			}
		}
	*/

	for i := 0; i < nReduce; i++ {
		oname := fmt.Sprintf("mr-%v-%v", t.TaskId, i)
		file, _ := ioutil.TempFile("", "temp-*")
		os.Rename(file.Name(), oname)
		enc := json.NewEncoder(file)
		defer file.Close()
		for _, kv := range intermediate[i] {
			//fmt.Println("key and value is ", kv)
			err := enc.Encode(&kv)
			if err != nil {
				panic("error")
			}
		}
	}

}

func Callstruct() {
	// declare an argument structure.
	args := Taskinfo{}

	fmt.Println("args is %T\n", args)
	// fill in the argument(s).
	args.RPCtype = REQUEST

	// declare a reply structure.
	reply := Taskinfo{}

	// send the RPC request, wait for the reply.
	call("Coordinator.Handlers", &args, &reply)

	// reply.Y should be 100.
	fmt.Printf("Task type is %v\n", reply.RPCtype)
}

//
// example function to show how to make an RPC call to the coordinator.
//
// the RPC argument and reply types are defined in rpc.go.
//
func CallExample() {

	// declare an argument structure.
	args := ExampleArgs{}

	// fill in the argument(s).
	args.X = 99

	// declare a reply structure.
	reply := ExampleReply{}

	// send the RPC request, wait for the reply.
	call("Coordinator.handlers", &args, &reply)

	// reply.Y should be 100.
	fmt.Printf("reply.Y %v\n", reply.Y)
}

//
// send an RPC request to the coordinator, wait for the response.
// usually returns true.
// returns false if something goes wrong.
//
func call(rpcname string, args interface{}, reply interface{}) bool {
	// c, err := rpc.DialHTTP("tcp", "127.0.0.1"+":1234")
	sockname := coordinatorSock()
	c, err := rpc.DialHTTP("unix", sockname)
	if err != nil {
		log.Fatal("dialing:", err)
	}
	defer c.Close()

	err = c.Call(rpcname, args, reply)
	if err == nil {
		return true
	}

	fmt.Println(err)
	return false
}
