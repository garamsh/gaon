package scheduler

import (
	"fmt"

	"github.com/garamsh/gaon/lib/api"
)

// volumeNeed is what the Volumes a MicroVM names ask of its Node.
type volumeNeed struct {
	// node is the Node the MicroVM's pinned local Volumes name, empty when
	// none is pinned.
	node string
	// refusal is why no Node can take the MicroVM, empty when one can.
	refusal string
}

// needOf reads the Volumes m names from volumes, by name. A Volume that does
// not exist, one being deleted, and local Volumes pinned to two Nodes each
// leave the MicroVM no Node: the API server would refuse the binding.
func needOf(m *api.MicroVM, volumes map[string]*api.Volume) volumeNeed {
	var need volumeNeed
	var pinnedBy string
	for _, mv := range m.Spec.Volumes {
		v, ok := volumes[mv.Name]
		switch {
		case !ok:
			return volumeNeed{refusal: fmt.Sprintf("volume %q does not exist", mv.Name)}
		case v.Metadata.DeletionTimestamp != nil:
			return volumeNeed{refusal: fmt.Sprintf("volume %q is being deleted", mv.Name)}
		case v.Spec.StorageClass != api.StorageClassLocal || v.Spec.NodeName == "":
			continue
		case need.node != "" && need.node != v.Spec.NodeName:
			return volumeNeed{refusal: fmt.Sprintf("volumes %q and %q are pinned to different Nodes", pinnedBy, mv.Name)}
		}
		need.node, pinnedBy = v.Spec.NodeName, mv.Name
	}
	return need
}
