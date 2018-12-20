/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package state

import (
	"fmt"
	"path"
	"sync"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
)

var _ State = &stateCheckpoint{}

type stateCheckpoint struct {
	mux               sync.RWMutex
	policyName        string
	cache             State
	checkpointManager checkpointmanager.CheckpointManager
	checkpointName    string
}

// NewCheckpointState creates new State for keeping track of cpu/pod assignment with checkpoint backend
func NewCheckpointState(stateDir, checkpointName, policyName string) (State, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}
	stateCheckpoint := &stateCheckpoint{
		cache:             NewMemoryState(),
		policyName:        policyName,
		checkpointManager: checkpointManager,
		checkpointName:    checkpointName,
	}

	if err := stateCheckpoint.restoreState(); err != nil {
		return nil, fmt.Errorf("could not restore state from checkpoint: %v\n"+
			"Please drain this node and delete the CPU manager checkpoint file %q before restarting Kubelet.",
			err, path.Join(stateDir, checkpointName))
	}

	return stateCheckpoint, nil
}

// restores state from a checkpoint and creates it if it doesn't exist
func (sc *stateCheckpoint) restoreState() error {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	var err error

	// used when all parsing is ok
	tmpAssignments := make(ContainerCPUAssignments)
	tmpDefaultCPUSet := cpuset.NewCPUSet()
	tmpContainerCPUSet := cpuset.NewCPUSet()

	checkpoint := NewCPUManagerCheckpoint()
	if err = sc.checkpointManager.GetCheckpoint(sc.checkpointName, checkpoint); err != nil {
		if err == errors.ErrCheckpointNotFound {
			sc.storeState()
			return nil
		}
		return err
	}

	if sc.policyName != checkpoint.PolicyName {
		return fmt.Errorf("configured policy %q differs from state checkpoint policy %q", sc.policyName, checkpoint.PolicyName)
	}

	if tmpDefaultCPUSet, err = cpuset.Parse(checkpoint.DefaultCPUSet); err != nil {
		return fmt.Errorf("could not parse default cpu set %q: %v", checkpoint.DefaultCPUSet, err)
	}

	for containerID, cpuString := range checkpoint.Entries {
		if tmpContainerCPUSet, err = cpuset.Parse(cpuString); err != nil {
			return fmt.Errorf("could not parse cpuset %q for container id %q: %v", cpuString, containerID, err)
		}
		tmpAssignments[containerID] = tmpContainerCPUSet
	}

	sc.cache.SetDefaultCPUSet(tmpDefaultCPUSet)
	sc.cache.SetCPUAssignments(tmpAssignments)
	sc.cache.SetPolicyData(checkpoint.PolicyData)

	glog.V(2).Info("[cpumanager] state checkpoint: restored state from checkpoint")
	glog.V(2).Infof("[cpumanager] state checkpoint: defaultCPUSet: %s", tmpDefaultCPUSet.String())

	return nil
}

// saves state to a checkpoint, caller is responsible for locking
func (sc *stateCheckpoint) storeState() {
	checkpoint := NewCPUManagerCheckpoint()
	checkpoint.PolicyName = sc.policyName
	checkpoint.DefaultCPUSet = sc.cache.GetDefaultCPUSet().String()
	checkpoint.PolicyData = sc.cache.GetPolicyData()

	for containerID, cset := range sc.cache.GetCPUAssignments() {
		checkpoint.Entries[containerID] = cset.String()
	}

	err := sc.checkpointManager.CreateCheckpoint(sc.checkpointName, checkpoint)

	if err != nil {
		panic("[cpumanager] could not save checkpoint: " + err.Error())
	}
}

// GetCPUSet returns current CPU set
func (sc *stateCheckpoint) GetCPUSet(containerID string) (cpuset.CPUSet, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	res, ok := sc.cache.GetCPUSet(containerID)
	return res, ok
}

// GetDefaultCPUSet returns default CPU set
func (sc *stateCheckpoint) GetDefaultCPUSet() cpuset.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetDefaultCPUSet()
}

// GetCPUSetOrDefault returns current CPU set, or default one if it wasn't changed
func (sc *stateCheckpoint) GetCPUSetOrDefault(containerID string) cpuset.CPUSet {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUSetOrDefault(containerID)
}

// GetCPUAssignments returns current CPU to pod assignments
func (sc *stateCheckpoint) GetCPUAssignments() ContainerCPUAssignments {
	sc.mux.RLock()
	defer sc.mux.RUnlock()

	return sc.cache.GetCPUAssignments()
}

// GetPolicyData returns the current opaque policy data, if any.
func (sc *stateCheckpoint) GetPolicyData() map[string]string {
	sc.mux.RLock()
	defer sc.mux.RUnlock()
	return sc.cache.GetPolicyData()
}

// GetPolicEntry returns opaque policy data for a particular key.
func (sc *stateCheckpoint) GetPolicyEntry(key string) (string, bool) {
	sc.mux.RLock()
	defer sc.mux.RUnlock()
	return sc.cache.GetPolicyEntry(key)
}

// GetPolicyEntryTo returns opaque policy data for a key as a given object.
func (sc *stateCheckpoint) GetPolicyEntryTo(key string, obj interface{}) bool {
        sc.mux.RLock()
        defer sc.mux.RUnlock()
        return sc.cache.GetPolicyEntryTo(key, obj)
}

// SetCPUSet sets CPU set
func (sc *stateCheckpoint) SetCPUSet(containerID string, cset cpuset.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUSet(containerID, cset)
	sc.storeState()
}

// SetDefaultCPUSet sets default CPU set
func (sc *stateCheckpoint) SetDefaultCPUSet(cset cpuset.CPUSet) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetDefaultCPUSet(cset)
	sc.storeState()
}

// SetCPUAssignments sets CPU to pod assignments
func (sc *stateCheckpoint) SetCPUAssignments(a ContainerCPUAssignments) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetCPUAssignments(a)
	sc.storeState()
}

// SetPolicyData sets the full set of opaque policy data.
func (sc *stateCheckpoint) SetPolicyData(data map[string]string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetPolicyData(data)
	sc.storeState()
}

// SetPolicyEntry sets the opaque data for a particular key.
func (sc *stateCheckpoint) SetPolicyEntry(key, value string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetPolicyEntry(key, value)
	sc.storeState()
}

// SetPolicyEntryFrom sets the opaque policy entry to an object.
func (sc *stateCheckpoint) SetPolicyEntryFrom(key string, obj interface{}) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.SetPolicyEntryFrom(key, obj)
	sc.storeState()
	glog.Infof("[cpumanager] updated policy entry \"%s\" = %+v", key, obj)
}

// Delete deletes assignment for specified pod
func (sc *stateCheckpoint) Delete(containerID string) {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.Delete(containerID)
	sc.storeState()
}

// ClearState clears the state and saves it in a checkpoint
func (sc *stateCheckpoint) ClearState() {
	sc.mux.Lock()
	defer sc.mux.Unlock()
	sc.cache.ClearState()
	sc.storeState()
}
