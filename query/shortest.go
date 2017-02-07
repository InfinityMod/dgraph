package query

import (
	"container/heap"
	"context"
	"fmt"

	"github.com/dgraph-io/dgraph/task"
	"github.com/dgraph-io/dgraph/x"
)

type Item struct {
	uid   uint64  // uid of the node.
	cost  float64 // cost of taking the path till this uid.
	hop   int     // number of hops taken to reach this node.
	index int
}

type priorityQueue []*Item

func (h priorityQueue) Len() int           { return len(h) }
func (h priorityQueue) Less(i, j int) bool { return h[i].cost < h[j].cost }
func (h priorityQueue) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *priorityQueue) Push(x interface{}) {
	n := len(*h)
	item := x.(*Item)
	item.index = n
	*h = append(*h, item)
}

func (h *priorityQueue) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func execNextLevel(ctx context.Context, start *SubGraph, next chan struct{}, res chan uint64, rch chan error) {
	var exec []*SubGraph
	var err error
	start.SrcUIDs = &task.List{[]uint64{start.Params.From}}
	start.uidMatrix = []*task.List{&task.List{Uids: []uint64{start.Params.From}}}
	start.DestUIDs = start.SrcUIDs

	for _, child := range start.Children {
		child.SrcUIDs = start.DestUIDs
		exec = append(exec, child)
	}
	dummy := &SubGraph{}
	for {
		<-next
		fmt.Println("***")
		rrch := make(chan error, len(exec))
		for _, sg := range exec {
			fmt.Println(sg.Attr)
			go ProcessGraph(ctx, sg, dummy, rrch)
		}

		for _ = range exec {
			select {
			case err = <-rrch:
				if err != nil {
					x.TraceError(ctx, x.Wrapf(err, "Error while processing child task"))
					rch <- err
					return
				}
			case <-ctx.Done():
				x.TraceError(ctx, x.Wrapf(ctx.Err(), "Context done before full execution"))
				rch <- ctx.Err()
				return
			}
		}
		rch <- nil

		for _, sg := range exec {
			// Send the destuids in res chan.
			for _, uid := range sg.DestUIDs.Uids {
				res <- uid
			}
		}

		res <- 0
		// modify the parents and exec.
		var out []*SubGraph
		for _, sg := range exec {
			for _, child := range start.Children {
				temp := new(SubGraph)
				*temp = *child
				*temp.SrcUIDs = *sg.DestUIDs
				sg.Children = append(sg.Children, temp)
				out = append(out, temp)
			}
		}

		exec = out
	}
}

func ShortestPath(ctx context.Context, sg *SubGraph, rch chan error) {
	var err error
	if sg.Params.Alias != "shortest" {
		rch <- nil
		return
	}

	pq := make(priorityQueue, 0)
	heap.Init(&pq)

	srcNode := &Item{sg.Params.From, 0, 0, 0}

	heap.Push(&pq, srcNode)

	var finalCost float64
	numHops := -1

	next := make(chan struct{})
	rch1 := make(chan error)
	res := make(chan uint64, 1000)
	go execNextLevel(ctx, sg, next, res, rch1)

	for pq.Len() > 0 {
		item := heap.Pop(&pq).(*Item)
		fmt.Println("Top queue: ", item.uid, item.hop, numHops)
		if item.uid == sg.Params.To {
			finalCost = item.cost
			break
		}
		if item.hop > numHops {
			// Explore the next level by calling processGraph and add them
			// to the queue.
			next <- struct{}{}

			select {
			case err = <-rch1:
				if err != nil {
					x.TraceError(ctx, x.Wrapf(err, "Error while processing child task"))
					rch <- err
					return
				}
			case <-ctx.Done():
				x.TraceError(ctx, x.Wrapf(ctx.Err(), "Context done before full execution"))
				rch <- ctx.Err()
				return
			}

			for it := range res {
				if it == 0 {
					break
				}
				fmt.Println(it, "###")
				node := &Item{it, item.cost + 1, item.hop + 1, 0}
				heap.Push(&pq, node)
			}
			numHops++
		}

		fmt.Println(item.uid, item.cost)
	}

	// Go through the execution tree to find the path.
	result := postTraverse(sg, finalCost)
	fmt.Println(result.Uids)
	sg.DestUIDs = result
	rch <- nil
}

func postTraverse(sg *SubGraph, cost float64) *task.List {
	from := sg.Params.From
	to := sg.Params.To

	res := new(task.List)
	res.Uids = append(res.Uids, from)
	res.Uids = append(res.Uids, to)

	return res
}
