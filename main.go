package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strings"

	"github.com/NHAS/checkpoint-audit/table"
)

type Node struct {
	Uid      string
	Name     string
	Comments string
	Type     string

	IPv4          string `json:"ipv4-address"`
	SubnetAddress string `json:"subnet4"`
	MaskLength    int    `json:"mask-length4"`
	Port          string
	Protocol      string
	Members       []string

	Edges []*Edge
}

type Edge struct {
	Start  *Node
	End    *Node
	Method string
}

type ACLRule struct {
	Action      string
	Name        string
	SrcNegate   bool `json:"source-negate"`
	DstNegate   bool `json:"destination-negate"`
	Comments    string
	Source      []string
	Destination []string
	Type        string
	Enabled     bool
	Number      int `json:"rule-number"`
	Service     []string
}

func Bidirectional(n1 *Node, n2 *Node) {
	to := Edge{Start: n1, End: n2, Method: "Di"}
	from := Edge{Start: n2, End: n1, Method: "Di"}

	n1.Edges = append(n1.Edges, &to)
	n2.Edges = append(n2.Edges, &from)
}

func Monodirectional(to *Node, from *Node) {
	e := Edge{Start: from, End: to, Method: "Mono"}

	to.Edges = append(to.Edges, &e)
	from.Edges = append(from.Edges, &e)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {

	objPath := flag.String("objs", "", "Objects file")
	acls := flag.String("acls", "", "ACL file")
	target := flag.String("t", "", "Target node (by name)")

	flag.Parse()

	objs, err := ioutil.ReadFile(*objPath)
	check(err)

	var jsonObjects []json.RawMessage
	check(json.Unmarshal(objs, &jsonObjects))

	nameMap := make(map[string]string)

	allObjects := make(map[string]*Node)

	groups := []*Node{}
	networks := []*Node{}
	hosts := []*Node{}

	//Populate all objects
	for _, v := range jsonObjects {
		var n Node
		check(json.Unmarshal(v, &n))
		allObjects[n.Uid] = &n

		switch n.Type {
		case "host":
			hosts = append(hosts, &n)
		case "group", "service-group":
			groups = append(groups, &n)
		case "network":
			networks = append(networks, &n)
		}

		nameMap[n.Name] = n.Uid
	}

	//Dereference objects and populate groups
	for g := range groups {
		for _, m := range groups[g].Members {
			Monodirectional(allObjects[m], groups[g])
		}
	}

	for n := range networks {
		for h := range hosts {
			_, netRange, err := net.ParseCIDR(fmt.Sprintf("%s/%d", networks[n].SubnetAddress, networks[n].MaskLength))
			check(err)

			if netRange.Contains(net.ParseIP(hosts[h].IPv4)) {
				Bidirectional(hosts[h], networks[n])
			}
		}
	}

	associatedNodes := getAssociatedNodes(allObjects[nameMap[*target]])

	t, err := table.NewTable(*target+" Belongs To", "Name", "Type", "Extra", "Comment", "UID")
	check(err)

	for _, currentNode := range associatedNodes {

		extraData := ""
		switch currentNode.Type {
		case "host":
			extraData = currentNode.IPv4
		case "network":
			extraData = fmt.Sprintf("%s/%d", currentNode.SubnetAddress, currentNode.MaskLength)
		case "group":
			extraData = fmt.Sprintf("Members %d", len(currentNode.Members))
		}

		t.AddValues(currentNode.Name, currentNode.Type, extraData, strings.TrimSpace(currentNode.Comments), currentNode.Uid)
	}

	t.Print()

	checkMap := make(map[string]bool)
	for _, n := range associatedNodes {
		checkMap[n.Uid] = true
	}

	aclBytes, err := ioutil.ReadFile(*acls)
	check(err)

	var rules []json.RawMessage
	check(json.Unmarshal(aclBytes, &rules))

	var accessTo []ACLRule
	var accessFrom []ACLRule

OuterLoop:
	for _, r := range rules {
		if bytes.Contains(r, []byte("access-rule")) {
			var acl ACLRule
			check(json.Unmarshal(r, &acl))
			if acl.Enabled {

				for _, uid := range acl.Source {
					if isAssociated(checkMap, allObjects, acl, uid) && !acl.SrcNegate {
						accessTo = append(accessTo, acl)
						continue OuterLoop
					}
				}

				for _, uid := range acl.Destination {
					if isAssociated(checkMap, allObjects, acl, uid) && !acl.DstNegate {
						accessFrom = append(accessFrom, acl)
						continue OuterLoop
					}
				}

			}
		}
	}

	fmt.Print("\n")

	accessToTable, _ := table.NewTable(*target+"->Target", "No.", "Src", "Dst", "Service")
	buildTable(&accessToTable, accessTo, allObjects)
	accessToTable.Print()

	fmt.Print("\n")

	accessFromTable, _ := table.NewTable("Target->"+*target, "No.", "Src", "Dst", "Service")
	buildTable(&accessFromTable, accessFrom, allObjects)
	accessFromTable.Print()

}

func getAssociatedNodes(n *Node) (assoc []*Node) {
	visited := make(map[*Node]bool)

	visited[n] = true
	searchSpace := []*Node{n}
	//Only add directly connected networks
	for _, e := range n.Edges {
		if !visited[e.End] && e.End.Type == "network" {
			visited[e.End] = true
			searchSpace = append(searchSpace, e.End)
		}
	}

	for len(searchSpace) != 0 {
		currentNode := searchSpace[0]
		assoc = append(assoc, currentNode)
		searchSpace = searchSpace[1:]

		for _, e := range currentNode.Edges {
			if visited[e.Start] {
				continue
			}

			searchSpace = append(searchSpace, e.Start)
			visited[e.Start] = true

		}
	}

	return
}

func isAssociated(associatedObjects map[string]bool, allObjects map[string]*Node, acl ACLRule, uid string) bool {
	return (associatedObjects[uid] || allObjects[uid].Type == "CpmiAnyObject") &&
		allObjects[acl.Action].Name == "Accept"
}

func buildTable(table *table.Table, acl []ACLRule, allObjects map[string]*Node) {
	for _, aclr := range acl {

		src := ""
		for _, v := range aclr.Source {
			if aclr.SrcNegate {
				src += "!"
			}

			src += allObjects[v].Name + "\n"

		}
		src = src[:len(src)-1]

		dst := ""
		for _, v := range aclr.Destination {
			if aclr.DstNegate {
				dst += "!"
			}

			dst += allObjects[v].Name + "\n"

		}
		dst = dst[:len(dst)-1]

		service := ""
		for _, v := range aclr.Service {
			serv := allObjects[v]

			if strings.Contains(serv.Type, "group") {
				for _, member := range serv.Members {
					subservice := allObjects[member]
					service += subservice.Name + ":" + subservice.Type
					if !strings.Contains(subservice.Type, "icmp") {
						service += ":" + subservice.Port
					}
					service += "\n"
				}
				continue
			}

			service += serv.Name + ":" + serv.Type
			if !strings.Contains(serv.Type, "icmp") {
				service += ":" + serv.Port
			}
			service += "\n"

		}

		err := table.AddValues(fmt.Sprintf("%d", aclr.Number), src, dst, service)
		check(err)

	}

}
