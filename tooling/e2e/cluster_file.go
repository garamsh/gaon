package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/garamsh/gaon/lib/api"
)

// ClusterFileVersion is the cluster.json version this package reads.
const ClusterFileVersion = 1

var imageReference = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)

// Node is a QEMU Node of the cluster.
type Node struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	SerialLog string `json:"serialLog"`
	// ControlPlane is true on the Node that runs etcd and the control plane.
	ControlPlane bool `json:"controlPlane"`
}

// Cluster is the cluster naru-cluster booted, as its cluster.json describes it.
type Cluster struct {
	// APIRoot is the root of the resource API as the test host reaches it.
	APIRoot string
	CA      string
	Cert    string
	Key     string
	// ControlPlane is the one Node that runs the control plane.
	ControlPlane Node
	// Workers are the other Nodes, in cluster.json's order; there is at least one.
	Workers []Node
	// Image is the reference of an image every Node's mvmd can pull by digest.
	Image string
}

type clusterFile struct {
	Version   int `json:"version"`
	APIServer struct {
		Address string `json:"address"`
	} `json:"apiServer"`
	PKI struct {
		CA         string `json:"ca"`
		ClientCert string `json:"clientCert"`
		ClientKey  string `json:"clientKey"`
	} `json:"pki"`
	Nodes  []Node `json:"nodes"`
	Images []struct {
		Reference string `json:"reference"`
	} `json:"images"`
}

// ReadClusterFile parses the cluster.json at path. It refuses a version other
// than ClusterFileVersion, a file with no API address or client pair, a file
// without exactly one control-plane Node and one other Node at least, and an
// image reference that is not repository@sha256:<digest>.
func ReadClusterFile(path string) (Cluster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Cluster{}, fmt.Errorf("read cluster file: %w", err)
	}
	var f clusterFile
	if err := json.Unmarshal(b, &f); err != nil {
		return Cluster{}, fmt.Errorf("parse cluster file %s: %w", path, err)
	}
	if f.Version != ClusterFileVersion {
		return Cluster{}, fmt.Errorf("cluster file %s has version %d, want %d", path, f.Version, ClusterFileVersion)
	}
	if f.APIServer.Address == "" || f.PKI.CA == "" || f.PKI.ClientCert == "" || f.PKI.ClientKey == "" {
		return Cluster{}, fmt.Errorf("cluster file %s names no API server address or no client pair", path)
	}
	if len(f.Images) == 0 || !imageReference.MatchString(f.Images[0].Reference) {
		return Cluster{}, fmt.Errorf("cluster file %s lists no image by digest", path)
	}
	c := Cluster{
		APIRoot: "https://" + f.APIServer.Address + "/apis/" + api.GroupVersion,
		CA:      f.PKI.CA,
		Cert:    f.PKI.ClientCert,
		Key:     f.PKI.ClientKey,
		Image:   f.Images[0].Reference,
	}
	planes := 0
	for _, n := range f.Nodes {
		if n.ControlPlane {
			c.ControlPlane = n
			planes++
			continue
		}
		c.Workers = append(c.Workers, n)
	}
	if planes != 1 || len(c.Workers) == 0 {
		return Cluster{}, fmt.Errorf("cluster file %s has %d control-plane Nodes and %d others, want 1 and at least 1", path, planes, len(c.Workers))
	}
	return c, nil
}
