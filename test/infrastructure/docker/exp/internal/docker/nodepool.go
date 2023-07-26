/*
Copyright 2020 The Kubernetes Authors.

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

// Package docker implements docker functionality.
package docker

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/blang/semver"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	expv1 "sigs.k8s.io/cluster-api/exp/api/v1beta1"
	infraexpv1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/exp/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/internal/docker"
	"sigs.k8s.io/cluster-api/test/infrastructure/kind"
	"sigs.k8s.io/cluster-api/util"
)

const (
	dockerMachinePoolLabel = "docker.cluster.x-k8s.io/machine-pool"
)

// NodePool is a wrapper around a collection of like machines which are owned by a DockerMachinePool. A node pool
// provides a friendly way of managing (adding, deleting, updating) a set of docker machines. The node pool will also
// sync the docker machine pool status Instances field with the state of the docker machines.
type NodePool struct {
	client            client.Client
	cluster           *clusterv1.Cluster
	machinePool       *expv1.MachinePool
	dockerMachinePool *infraexpv1.DockerMachinePool
	labelFilters      map[string]string
	nodePoolMachines  NodePoolMachines // Note: This must be initialized when creating a new node pool and updated to reflect the `machines` slice.
}

// NodePoolMachine is a wrapper around a docker.Machine and a NodePoolMachineStatus, which maintains additional information about the machine.
// At least one of Machine or Status must be non-nil.
type NodePoolMachine struct {
	Machine *docker.Machine
	Status  *NodePoolMachineStatus
}

// NodePoolMachineStatus is a representation of the additional information needed to reconcile a docker.Machine.
// This is the only source of truth for understanding if the machine is already bootstrapped, ready, etc. so this
// information must be maintained through reconciliation.
type NodePoolMachineStatus struct {
	Name             string
	PrioritizeDelete bool
}

// NodePoolMachines is a sortable slice of NodePoolMachine based on the deletion priority.
// Machines with PrioritizeDelete set to true will be sorted to the front of the list.
type NodePoolMachines []NodePoolMachine

func (n NodePoolMachines) Len() int      { return len(n) }
func (n NodePoolMachines) Swap(i, j int) { n[i], n[j] = n[j], n[i] }
func (n NodePoolMachines) Less(i, j int) bool {
	var prioritizeI, prioritizeJ bool
	var nameI, nameJ string
	if n[i].Status != nil {
		prioritizeI = n[i].Status.PrioritizeDelete
		nameI = n[i].Status.Name
	} else {
		nameI = n[i].Machine.Name()
	}

	if n[j].Status != nil {
		prioritizeJ = n[j].Status.PrioritizeDelete
		nameJ = n[j].Status.Name
	} else {
		nameJ = n[j].Machine.Name()
	}

	if prioritizeI == prioritizeJ {
		return nameI < nameJ
	}

	return prioritizeI
}

// NewNodePool creates a new node pool.
func NewNodePool(ctx context.Context, c client.Client, cluster *clusterv1.Cluster, mp *expv1.MachinePool, dmp *infraexpv1.DockerMachinePool, nodePoolMachineStatuses []NodePoolMachineStatus) (*NodePool, error) {
	log := ctrl.LoggerFrom(ctx)
	np := &NodePool{
		client:            c,
		cluster:           cluster,
		machinePool:       mp,
		dockerMachinePool: dmp,
		labelFilters:      map[string]string{dockerMachinePoolLabel: dmp.Name},
	}

	log.Info("NewNodePool got nodePoolMachineStatuses", "nodePoolMachineStatuses", nodePoolMachineStatuses)
	np.nodePoolMachines = make([]NodePoolMachine, 0, len(nodePoolMachineStatuses))
	for i := range nodePoolMachineStatuses {
		np.nodePoolMachines = append(np.nodePoolMachines, NodePoolMachine{
			Status: &nodePoolMachineStatuses[i],
		})
	}
	log.Info("nodePoolMachines init with statuses and no machine", "nodePoolMachines", np.nodePoolMachines)

	if err := np.refresh(ctx); err != nil {
		return np, errors.Wrapf(err, "failed to refresh the node pool")
	}
	return np, nil
}

// GetNodePoolMachineStatuses returns the node pool machines providing the information to construct DockerMachines.
func (np *NodePool) GetNodePoolMachineStatuses() []NodePoolMachineStatus {
	statusList := make([]NodePoolMachineStatus, len(np.nodePoolMachines))
	for i, nodePoolMachine := range np.nodePoolMachines {
		statusList[i] = *nodePoolMachine.Status
	}

	return statusList
}

// ReconcileMachines will build enough machines to satisfy the machine pool / docker machine pool spec
// eventually delete all the machine in excess, and update the status for all the machines.
//
// NOTE: The goal for the current implementation is to verify MachinePool construct; accordingly,
// currently the nodepool supports only a recreate strategy for replacing old nodes with new ones
// (all existing machines are killed before new ones are created).
// TODO: consider if to support a Rollout strategy (a more progressive node replacement).
func (np *NodePool) ReconcileMachines(ctx context.Context) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	desiredReplicas := int(*np.machinePool.Spec.Replicas)

	// Delete all the machines in excess (outdated machines or machines exceeding desired replica count).
	machineDeleted := false
	totalNumberOfMachines := 0
	// We start deleting machines from the front of the list, so we need to sort the machines prioritized for deletion to the beginning.
	for _, nodePoolMachine := range np.nodePoolMachines {
		machine := nodePoolMachine.Machine
		totalNumberOfMachines++
		if totalNumberOfMachines > desiredReplicas || !np.isMachineMatchingInfrastructureSpec(machine) {
			externalMachine, err := docker.NewMachine(ctx, np.cluster, machine.Name(), np.labelFilters)
			if err != nil {
				return ctrl.Result{}, errors.Wrapf(err, "failed to create helper for managing the externalMachine named %s", machine.Name())
			}
			if err := externalMachine.Delete(ctx); err != nil {
				return ctrl.Result{}, errors.Wrapf(err, "failed to delete machine %s", machine.Name())
			}
			machineDeleted = true
			totalNumberOfMachines-- // remove deleted machine from the count
		}
	}
	if machineDeleted {
		if err := np.refresh(ctx); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to refresh the node pool")
		}
	}

	// Add new machines if missing.
	machineAdded := false
	matchingMachineCount := len(np.machinesMatchingInfrastructureSpec())
	log.Info("MatchingMachineCount", "count", matchingMachineCount)
	if matchingMachineCount < desiredReplicas {
		for i := 0; i < desiredReplicas-matchingMachineCount; i++ {
			if err := np.addMachine(ctx); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to create a new docker machine")
			}
			machineAdded = true
		}
	}
	if machineAdded {
		if err := np.refresh(ctx); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to refresh the node pool")
		}
	}

	return ctrl.Result{}, nil
}

// Delete will delete all of the machines in the node pool.
func (np *NodePool) Delete(ctx context.Context) error {
	for _, nodePoolMachine := range np.nodePoolMachines {
		machine := nodePoolMachine.Machine
		externalMachine, err := docker.NewMachine(ctx, np.cluster, machine.Name(), np.labelFilters)
		if err != nil {
			return errors.Wrapf(err, "failed to create helper for managing the externalMachine named %s", machine.Name())
		}

		if err := externalMachine.Delete(ctx); err != nil {
			return errors.Wrapf(err, "failed to delete machine %s", machine.Name())
		}
	}

	// Note: We can set `np.nodePoolMachines = nil` here, but it shouldn't be necessary on Delete().

	return nil
}

func (np *NodePool) isMachineMatchingInfrastructureSpec(machine *docker.Machine) bool {
	// NOTE: With the current implementation we are checking if the machine is using a kindest/node image for the expected version,
	// but not checking if the machine has the expected extra.mounts or pre.loaded images.

	semVer, err := semver.Parse(strings.TrimPrefix(*np.machinePool.Spec.Template.Spec.Version, "v"))
	if err != nil {
		// TODO: consider if to return an error
		panic(errors.Wrap(err, "failed to parse DockerMachine version").Error())
	}

	kindMapping := kind.GetMapping(semVer, np.dockerMachinePool.Spec.Template.CustomImage)

	return machine.ContainerImage() == kindMapping.Image
}

// machinesMatchingInfrastructureSpec returns all of the docker.Machines which match the machine pool / docker machine pool spec.
func (np *NodePool) machinesMatchingInfrastructureSpec() []*docker.Machine {
	var matchingMachines []*docker.Machine
	for _, nodePoolMachine := range np.nodePoolMachines {
		machine := nodePoolMachine.Machine
		if np.isMachineMatchingInfrastructureSpec(machine) {
			matchingMachines = append(matchingMachines, machine)
		}
	}

	return matchingMachines
}

// addMachine will add a new machine to the node pool and update the docker machine pool status.
func (np *NodePool) addMachine(ctx context.Context) error {
	name := fmt.Sprintf("worker-%s", util.RandomString(6))
	externalMachine, err := docker.NewMachine(ctx, np.cluster, name, np.labelFilters)
	if err != nil {
		return errors.Wrapf(err, "failed to create helper for managing the externalMachine named %s", name)
	}

	// NOTE: FailureDomains don't mean much in CAPD since it's all local, but we are setting a label on
	// each container, so we can check placement.
	labels := map[string]string{}
	for k, v := range np.labelFilters {
		labels[k] = v
	}

	if len(np.machinePool.Spec.FailureDomains) > 0 {
		// For MachinePools placement is expected to be managed by the underlying infrastructure primitive, but
		// given that there is no such an thing in CAPD, we are picking a random failure domain.
		randomIndex := rand.Intn(len(np.machinePool.Spec.FailureDomains)) //nolint:gosec
		for k, v := range docker.FailureDomainLabel(&np.machinePool.Spec.FailureDomains[randomIndex]) {
			labels[k] = v
		}
	}

	if err := externalMachine.Create(ctx, np.dockerMachinePool.Spec.Template.CustomImage, constants.WorkerNodeRoleValue, np.machinePool.Spec.Template.Spec.Version, labels, np.dockerMachinePool.Spec.Template.ExtraMounts); err != nil {
		return errors.Wrapf(err, "failed to create docker machine with name %s", name)
	}
	return nil
}

// refresh asks docker to list all the machines matching the node pool label and updates the cached list of node pool
// machines.
func (np *NodePool) refresh(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx)
	log.Info("nodePoolMachines are", "nodePoolMachines", np.nodePoolMachines)

	// Construct a map of the existing nodePoolMachines so we can look up the status as it's the source of truth about the state of the docker.Machine.
	nodePoolMachineStatusMap := make(map[string]*NodePoolMachineStatus, len(np.nodePoolMachines))
	for i := range np.nodePoolMachines {
		var name string
		// At least one of Machine or Status should be non-nil.
		// When this is called in NewNodePool(), we expect Machine to be nil as we're passing in an existing list of statuses.
		// When this is being called during reconciliation, we expect both Machine and Status to be non-nil.
		if np.nodePoolMachines[i].Machine != nil {
			name = np.nodePoolMachines[i].Machine.Name()
		} else {
			name = np.nodePoolMachines[i].Status.Name
		}
		nodePoolMachineStatusMap[name] = np.nodePoolMachines[i].Status
	}

	// Update the list of machines
	machines, err := docker.ListMachinesByCluster(ctx, np.cluster, np.labelFilters)
	if err != nil {
		return errors.Wrapf(err, "failed to list all machines in the cluster")
	}
	log.Info("Machines by cluster")
	for _, machine := range machines {
		log.Info("Machine", "name", machine.Name())
	}

	// Construct a new list of nodePoolMachines but keep the existing status.
	np.nodePoolMachines = make(NodePoolMachines, 0, len(machines))
	for i := range machines {
		machine := machines[i]
		// makes sure no control plane machines gets selected by chance.
		if !machine.IsControlPlane() {
			nodePoolMachine := NodePoolMachine{
				Machine: machine,
			}
			if existingStatus, ok := nodePoolMachineStatusMap[machine.Name()]; ok {
				nodePoolMachine.Status = existingStatus
			} else {
				nodePoolMachine.Status = &NodePoolMachineStatus{
					Name: nodePoolMachine.Machine.Name(),
				}
			}
			np.nodePoolMachines = append(np.nodePoolMachines, nodePoolMachine)
		}
	}

	sort.Sort(np.nodePoolMachines)

	return nil
}
