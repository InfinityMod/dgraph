/*
 * Copyright 2017 Dgraph Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * 		http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package query

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	geom "github.com/twpayne/go-geom"
	"github.com/twpayne/go-geom/encoding/geojson"

	"github.com/dgraph-io/dgraph/algo"
	"github.com/dgraph-io/dgraph/protos/graphp"
	"github.com/dgraph-io/dgraph/types"
	"github.com/dgraph-io/dgraph/x"
)

// ToProtocolBuf returns the list of graphp.Node which would be returned to the go
// client.
func ToProtocolBuf(l *Latency, sgl []*SubGraph) ([]*graphp.Node, error) {
	var resNode []*graphp.Node
	for _, sg := range sgl {
		if sg.Params.Alias == "var" || sg.Params.Alias == "shortest" {
			continue
		}
		node, err := sg.ToProtocolBuffer(l)
		if err != nil {
			return nil, err
		}
		resNode = append(resNode, node)
	}
	return resNode, nil
}

// ToJson converts the list of subgraph into a JSON response by calling ToFastJSON.
func ToJson(l *Latency, sgl []*SubGraph, w io.Writer, allocIds map[string]string) error {
	sgr := &SubGraph{
		Attr: "__",
	}
	for _, sg := range sgl {
		if sg.Params.Alias == "var" || sg.Params.Alias == "shortest" {
			continue
		}
		if sg.Params.isDebug {
			sgr.Params.isDebug = true
		}
		sgr.Children = append(sgr.Children, sg)
	}
	return sgr.ToFastJSON(l, w, allocIds)
}

// outputNode is the generic output / writer for preTraverse.
type outputNode interface {
	AddValue(attr string, v types.Val)
	AddMapChild(attr string, node outputNode, isRoot bool)
	AddListChild(attr string, child outputNode)
	New(attr string) outputNode
	SetUID(uid uint64)
	SetXID(xid string)
	IsEmpty() bool
}

// protoNode is the proto output for preTraverse.
type protoNode struct {
	*graphp.Node
}

// AddValue adds an attribute value for protoOutputNode.
func (p *protoNode) AddValue(attr string, v types.Val) {
	p.Node.Properties = append(p.Node.Properties, createProperty(attr, v))
}

// AddMapChild adds a node value for protoOutputNode.
func (p *protoNode) AddMapChild(attr string, v outputNode, isRoot bool) {
	// Assert that attr == v.Node.Attribute
	var childNode *graphp.Node
	var as []string
	for _, c := range p.Node.Children {
		as = append(as, c.Attribute)
		if c.Attribute == attr {
			childNode = c
			break
		}
	}
	if childNode != nil && isRoot {
		childNode.Children = append(childNode.Children, v.(*protoNode).Node)
	} else if childNode != nil {
		// merge outputNode into childNode
		vnode := v.(*protoNode).Node
		x.AssertTruef(vnode.Uid == childNode.Uid && vnode.Xid == childNode.Xid,
			"Invalid nodes while merging.")
		for _, p := range vnode.Properties {
			childNode.Properties = append(childNode.Properties, p)
		}
		for _, c := range vnode.Children {
			childNode.Children = append(childNode.Children, c)
		}
	} else {
		vParent := v
		if isRoot {
			vParent = v.New(attr)
			vParent.AddListChild(attr, v)
		}
		p.Node.Children = append(p.Node.Children, vParent.(*protoNode).Node)
	}
}

// AddListChild adds a child for protoOutputNode.
func (p *protoNode) AddListChild(attr string, child outputNode) {
	p.Node.Children = append(p.Node.Children, child.(*protoNode).Node)
}

// New creates a new node for protoOutputNode.
func (p *protoNode) New(attr string) outputNode {
	uc := nodePool.Get().(*graphp.Node)
	uc.Attribute = attr
	return &protoNode{uc}
}

// SetUID sets UID of a protoOutputNode.
func (p *protoNode) SetUID(uid uint64) { p.Node.Uid = uid }

// SetXID sets XID of a protoOutputNode.
func (p *protoNode) SetXID(xid string) { p.Node.Xid = xid }

func (p *protoNode) IsEmpty() bool {
	if p.Node.Uid > 0 {
		return false
	}
	if len(p.Node.Children) > 0 {
		return false
	}
	if len(p.Node.Properties) > 0 {
		return false
	}
	return true
}

func (n *protoNode) normalize(props []*graphp.Property, out []protoNode) []protoNode {
	if len(n.Children) == 0 {
		props = append(props, n.Properties...)
		pn := protoNode{&graphp.Node{Properties: props}}
		out = append(out, pn)
		return out
	}

	for _, child := range n.Children {
		p := make([]*graphp.Property, len(props))
		copy(p, props)
		p = append(p, n.Properties...)
		out = (&protoNode{child}).normalize(p, out)
	}
	return out
}

// ToProtocolBuffer does preorder traversal to build a proto buffer. We have
// used postorder traversal before, but preorder seems simpler and faster for
// most cases.
func (sg *SubGraph) ToProtocolBuffer(l *Latency) (*graphp.Node, error) {
	var seedNode *protoNode
	if sg.uidMatrix == nil {
		return seedNode.New(sg.Params.Alias).(*protoNode).Node, nil
	}

	n := seedNode.New("_root_")
	for _, uid := range sg.uidMatrix[0].Uids {
		// For the root, the name is stored in Alias, not Attr.
		if algo.IndexOf(sg.DestUIDs, uid) < 0 {
			// This UID was filtered. So Ignore it.
			continue
		}
		n1 := seedNode.New(sg.Params.Alias)
		if sg.Params.GetUID || sg.Params.isDebug {
			n1.SetUID(uid)
		}

		if rerr := sg.preTraverse(uid, n1, n1); rerr != nil {
			if rerr.Error() == "_INV_" {
				continue
			}
			return n.(*protoNode).Node, rerr
		}
		if n1.IsEmpty() {
			continue
		}
		if !sg.Params.Normalize {
			n.AddListChild(sg.Params.Alias, n1)
			continue
		}

		// Lets normalize the response now.
		normalized := make([]protoNode, 0, 10)
		props := make([]*graphp.Property, 0, 10)
		for _, c := range (n1.(*protoNode)).normalize(props, normalized) {
			n.AddListChild(sg.Params.Alias, &c)
		}
	}
	l.ProtocolBuffer = time.Since(l.Start) - l.Parsing - l.Processing
	return n.(*protoNode).Node, nil
}

type fastJsonAttr struct {
	isScalar  bool
	scalarVal []byte
	nodeVal   *fastJsonNode
}

func makeScalarAttr(val []byte) *fastJsonAttr {
	return &fastJsonAttr{true, val, nil}
}
func makeNodeAttr(val *fastJsonNode) *fastJsonAttr {
	return &fastJsonAttr{false, nil, val}
}

type fastJsonNode struct {
	children map[string][]*fastJsonNode
	attrs    map[string]*fastJsonAttr
}

func (fj *fastJsonNode) AddValue(attr string, v types.Val) {
	if bs, err := valToBytes(v); err == nil {
		_, found := fj.attrs[attr]
		x.AssertTruef(!found, "Setting value twice for same attribute")
		fj.attrs[attr] = makeScalarAttr(bs)
	}
}

func (fj *fastJsonNode) AddMapChild(attr string, val outputNode, _ bool) {
	nodeAttr, found := fj.attrs[attr]
	if found {
		if nodeAttr.isScalar {
			x.Fatalf("Can not merge scalar and node values.")
		}
		// merge val and nodeAttr.nodeVal
		for k, v := range val.(*fastJsonNode).children {
			nodeAttr.nodeVal.children[k] = v
		}
		for k, v := range val.(*fastJsonNode).attrs {
			nodeAttr.nodeVal.attrs[k] = v
		}
	} else {
		fj.attrs[attr] = makeNodeAttr(val.(*fastJsonNode))
	}
}

func (fj *fastJsonNode) AddListChild(attr string, child outputNode) {
	children, found := fj.children[attr]
	if !found {
		children = make([]*fastJsonNode, 0, 5)
	}
	fj.children[attr] = append(children, child.(*fastJsonNode))
}

func (fj *fastJsonNode) New(attr string) outputNode {
	return &fastJsonNode{
		children: make(map[string][]*fastJsonNode),
		attrs:    make(map[string]*fastJsonAttr),
	}
}

func (fj *fastJsonNode) SetUID(uid uint64) {
	uidBs, found := fj.attrs["_uid_"]
	if found {
		x.AssertTruef(uidBs.isScalar, "Found node value for _uid_. Expected scalar value.")
		lUidBs := len(uidBs.scalarVal)
		currUid, err := strconv.ParseUint(string(uidBs.scalarVal[1:lUidBs-1]), 0, 64)
		x.AssertTruef(err == nil && currUid == uid, "Setting two different uids on same node.")
	} else {
		fj.attrs["_uid_"] = makeScalarAttr([]byte(fmt.Sprintf("\"%#x\"", uid)))
	}
}

func (fj *fastJsonNode) SetXID(xid string) {
	fj.attrs["_xid_"] = makeScalarAttr([]byte(strconv.Quote(xid)))
}

func (fj *fastJsonNode) IsEmpty() bool {
	return len(fj.attrs) == 0 && len(fj.children) == 0
}

func valToBytes(v types.Val) ([]byte, error) {
	switch v.Tid {
	case types.BinaryID:
		return v.Value.([]byte), nil
	case types.Int32ID:
		return []byte(fmt.Sprintf("%d", v.Value)), nil
	case types.FloatID:
		return []byte(fmt.Sprintf("%f", v.Value)), nil
	case types.BoolID:
		if v.Value.(bool) == true {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case types.StringID, types.DefaultID:
		return []byte(fmt.Sprintf("%q", v.Value.(string))), nil
	case types.DateID:
		s := v.Value.(time.Time).Format("2006-01-02")
		return json.Marshal(s)
	case types.DateTimeID:
		return v.Value.(time.Time).MarshalJSON()
	case types.GeoID:
		return geojson.Marshal(v.Value.(geom.T))
	case types.UidID:
		return []byte(fmt.Sprintf("\"%#x\"", v.Value)), nil
	case types.PasswordID:
		return []byte(fmt.Sprintf("%q", v.Value.(string))), nil
	default:
		return nil, errors.New("unsupported types.Val.Tid")
	}
}

func (fj *fastJsonNode) encode(bufw *bufio.Writer) {
	allKeys := make([]string, 0, len(fj.attrs))
	for k := range fj.attrs {
		allKeys = append(allKeys, k)
	}
	for k := range fj.children {
		allKeys = append(allKeys, k)
	}
	sort.Strings(allKeys)

	bufw.WriteRune('{')
	first := true
	for _, k := range allKeys {
		if !first {
			bufw.WriteRune(',')
		}
		first = false
		bufw.WriteRune('"')
		bufw.WriteString(k)
		bufw.WriteRune('"')
		bufw.WriteRune(':')

		if v, ok := fj.attrs[k]; ok {
			if v.isScalar {
				bufw.Write(v.scalarVal)
			} else {
				v.nodeVal.encode(bufw)
			}
		} else {
			v := fj.children[k]
			first := true
			bufw.WriteRune('[')
			for _, vi := range v {
				if !first {
					bufw.WriteRune(',')
				}
				first = false
				vi.encode(bufw)
			}
			bufw.WriteRune(']')
		}
	}
	bufw.WriteRune('}')
}

func (n *fastJsonNode) normalize(av []attrVal, out []fastJsonNode) []fastJsonNode {
	if len(n.children) == 0 {
		// No more children nodes, lets copy the attrs to the slice and attach the
		// result to out.
		for k, v := range n.attrs {
			av = append(av, attrVal{k, v})
		}

		fn := fastJsonNode{
			attrs: make(map[string]*fastJsonAttr),
		}
		for _, pair := range av {
			fn.attrs[pair.attr] = pair.val
		}
		out = append(out, fn)
		return out
	}

	for _, child := range n.children {
		// n.children is a map of string -> []*fastJsonNode.
		for _, jn := range child {
			vals := make([]attrVal, len(av))
			copy(vals, av)
			// Create a copy of the attr-val slice, attach attrs and pass to children.
			for k, v := range n.attrs {
				vals = append(vals, attrVal{k, v})
			}
			out = jn.normalize(vals, out)
		}
	}
	return out
}

type attrVal struct {
	attr string
	val  *fastJsonAttr
}

func processNodeUids(n *fastJsonNode, sg *SubGraph) error {
	var seedNode *fastJsonNode
	if sg.uidMatrix == nil {
		return nil
	}
	lenList := len(sg.uidMatrix[0].Uids)
	for i := 0; i < lenList; i++ {
		uid := sg.uidMatrix[0].Uids[i]
		if algo.IndexOf(sg.DestUIDs, uid) < 0 {
			// This UID was filtered. So Ignore it.
			continue
		}

		n1 := seedNode.New(sg.Params.Alias)
		if sg.Params.GetUID || sg.Params.isDebug {
			n1.SetUID(uid)
		}
		if err := sg.preTraverse(uid, n1, n1); err != nil {
			if err.Error() == "_INV_" {
				continue
			}
			return err
		}
		if n1.IsEmpty() {
			continue
		}

		if !sg.Params.Normalize {
			n.AddListChild(sg.Params.Alias, n1)
			continue
		}

		// Lets normalize the response now.
		normalized := make([]fastJsonNode, 0, 10)

		// This slice is used to mantain the leaf nodes along a path while traversing
		// the Subgraph.
		av := make([]attrVal, 0, 10)
		for _, c := range (n1.(*fastJsonNode)).normalize(av, normalized) {
			n.AddListChild(sg.Params.Alias, &fastJsonNode{attrs: c.attrs})
		}
	}
	return nil
}

func (sg *SubGraph) ToFastJSON(l *Latency, w io.Writer, allocIds map[string]string) error {
	var seedNode *fastJsonNode
	n := seedNode.New("_root_")
	if sg.Attr == "__" {
		for _, sg := range sg.Children {
			err := processNodeUids(n.(*fastJsonNode), sg)
			if err != nil {
				return err
			}
		}
	} else {
		err := processNodeUids(n.(*fastJsonNode), sg)
		if err != nil {
			return err
		}
	}

	if sg.Params.isDebug {
		sl := seedNode.New("serverLatency").(*fastJsonNode)
		for k, v := range l.ToMap() {
			val := types.ValueForType(types.StringID)
			val.Value = v
			sl.AddValue(k, val)
		}
		n.AddMapChild("server_latency", sl, false)
	}

	if allocIds != nil && len(allocIds) > 0 {
		sl := seedNode.New("uids").(*fastJsonNode)
		for k, v := range allocIds {
			val := types.ValueForType(types.StringID)
			val.Value = v
			sl.AddValue(k, val)
		}
		n.AddMapChild("uids", sl, false)
	}

	bufw := bufio.NewWriter(w)
	n.(*fastJsonNode).encode(bufw)
	return bufw.Flush()
}
