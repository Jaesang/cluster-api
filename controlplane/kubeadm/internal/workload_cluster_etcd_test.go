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

package internal

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"

	"go.etcd.io/etcd/clientv3"
	pb "go.etcd.io/etcd/etcdserver/etcdserverpb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controlplane/kubeadm/internal/etcd"
	fake2 "sigs.k8s.io/cluster-api/controlplane/kubeadm/internal/etcd/fake"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWorkload_EtcdIsHealthy(t *testing.T) {
	g := NewWithT(t)

	workload := &Workload{
		Client: &fakeClient{
			get: map[string]interface{}{
				"kube-system/etcd-test-1": etcdPod("etcd-test-1", withReadyOption),
				"kube-system/etcd-test-2": etcdPod("etcd-test-2", withReadyOption),
				"kube-system/etcd-test-3": etcdPod("etcd-test-3", withReadyOption),
				"kube-system/etcd-test-4": etcdPod("etcd-test-4"),
			},
			list: &corev1.NodeList{
				Items: []corev1.Node{
					nodeNamed("test-1", withProviderID("my-provider-id-1")),
					nodeNamed("test-2", withProviderID("my-provider-id-2")),
					nodeNamed("test-3", withProviderID("my-provider-id-3")),
					nodeNamed("test-4", withProviderID("my-provider-id-4")),
				},
			},
		},
		etcdClientGenerator: &fakeEtcdClientGenerator{
			client: &etcd.Client{
				EtcdClient: &fake2.FakeEtcdClient{
					EtcdEndpoints: []string{},
					MemberListResponse: &clientv3.MemberListResponse{
						Members: []*pb.Member{
							{Name: "test-1", ID: uint64(1)},
							{Name: "test-2", ID: uint64(2)},
							{Name: "test-3", ID: uint64(3)},
						},
					},
					AlarmResponse: &clientv3.AlarmResponse{
						Alarms: []*pb.AlarmMember{},
					},
				},
			},
		},
	}
	ctx := context.Background()
	health, err := workload.EtcdIsHealthy(ctx)
	g.Expect(err).NotTo(HaveOccurred())

	for _, err := range health {
		g.Expect(err).NotTo(HaveOccurred())
	}
}

func TestUpdateEtcdVersionInKubeadmConfigMap(t *testing.T) {
	kubeadmConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeadmConfigKey,
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{
			clusterConfigurationKey: `
apiVersion: kubeadm.k8s.io/v1beta2
kind: ClusterConfiguration
etcd:
  local:
    dataDir: /var/lib/etcd
    imageRepository: "gcr.io/k8s/etcd"
    imageTag: "0.10.9"
`,
		},
	}

	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())

	tests := []struct {
		name                  string
		objs                  []runtime.Object
		imageRepo             string
		imageTag              string
		expectErr             bool
		expectedClusterConfig string
	}{
		{
			name:      "returns error if unable to find kubeadm-config",
			objs:      nil,
			expectErr: true,
		},
		{
			name:      "updates the config map",
			expectErr: false,
			objs:      []runtime.Object{kubeadmConfig},
			imageRepo: "gcr.io/imgRepo",
			imageTag:  "v1.0.1-sometag.1",
			expectedClusterConfig: `apiVersion: kubeadm.k8s.io/v1beta2
etcd:
  local:
    dataDir: /var/lib/etcd
    imageRepository: gcr.io/imgRepo
    imageTag: v1.0.1-sometag.1
kind: ClusterConfiguration
`,
		},
		{
			name:      "doesn't update the config map if there are no changes",
			expectErr: false,
			imageRepo: "gcr.io/k8s/etcd",
			imageTag:  "0.10.9",
			objs:      []runtime.Object{kubeadmConfig},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			fakeClient := fake.NewFakeClientWithScheme(scheme, tt.objs...)
			w := &Workload{
				Client: fakeClient,
			}
			ctx := context.TODO()
			err := w.UpdateEtcdVersionInKubeadmConfigMap(ctx, tt.imageRepo, tt.imageTag)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			if tt.expectedClusterConfig != "" {
				var actualConfig corev1.ConfigMap
				g.Expect(w.Client.Get(
					ctx,
					ctrlclient.ObjectKey{Name: kubeadmConfigKey, Namespace: metav1.NamespaceSystem},
					&actualConfig,
				)).To(Succeed())
				g.Expect(actualConfig.Data[clusterConfigurationKey]).To(Equal(tt.expectedClusterConfig))
			}
		})
	}
}

func TestRemoveEtcdMemberFromMachine(t *testing.T) {
	machine := &clusterv1.Machine{
		Status: clusterv1.MachineStatus{
			NodeRef: &corev1.ObjectReference{
				Name: "cp1",
			},
		},
	}
	cp1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp1",
			Namespace: "cp1",
			Labels: map[string]string{
				labelNodeRoleMaster: "",
			},
		},
	}
	cp1DiffNS := cp1.DeepCopy()
	cp1DiffNS.Namespace = "diff-ns"

	cp2 := cp1.DeepCopy()
	cp2.Name = "cp2"
	cp2.Namespace = "cp2"

	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
	tests := []struct {
		name                string
		machine             *clusterv1.Machine
		etcdClientGenerator etcdClientFor
		objs                []runtime.Object
		expectErr           bool
	}{
		{
			name:      "does not panic if machine is nil",
			expectErr: false,
		},
		{
			name: "does not panic if machine noderef is nil",
			machine: &clusterv1.Machine{
				Status: clusterv1.MachineStatus{
					NodeRef: nil,
				},
			},
			expectErr: false,
		},
		{
			name:      "returns error if there are less than 2 control plane nodes",
			machine:   machine,
			objs:      []runtime.Object{cp1},
			expectErr: true,
		},
		{
			name: "returns error if nodes match node ref name",
			machine: &clusterv1.Machine{
				Status: clusterv1.MachineStatus{
					NodeRef: &corev1.ObjectReference{
						Name: "cp1",
					},
				},
			},
			objs:      []runtime.Object{cp1, cp1DiffNS},
			expectErr: true,
		},
		{
			name:                "returns error if it failed to create etcdClient",
			machine:             machine,
			objs:                []runtime.Object{cp1, cp2},
			etcdClientGenerator: &fakeEtcdClientGenerator{err: errors.New("no client")},
			expectErr:           true,
		},
		{
			name:    "returns error if it failed to get etcd members",
			machine: machine,
			objs:    []runtime.Object{cp1, cp2},
			etcdClientGenerator: &fakeEtcdClientGenerator{
				client: &etcd.Client{
					EtcdClient: &fake2.FakeEtcdClient{
						ErrorResponse: errors.New("cannot get etcd members"),
					},
				},
			},
			expectErr: true,
		},
		{
			name:    "returns error if it failed to remove etcd member",
			machine: machine,
			objs:    []runtime.Object{cp1, cp2},
			etcdClientGenerator: &fakeEtcdClientGenerator{
				client: &etcd.Client{
					EtcdClient: &fake2.FakeEtcdClient{
						ErrorResponse: errors.New("cannot remove etcd member"),
						MemberListResponse: &clientv3.MemberListResponse{
							Members: []*pb.Member{
								{Name: "cp1", ID: uint64(1)},
								{Name: "test-2", ID: uint64(2)},
								{Name: "test-3", ID: uint64(3)},
							},
						},
						AlarmResponse: &clientv3.AlarmResponse{
							Alarms: []*pb.AlarmMember{},
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name:    "removes member from etcd",
			machine: machine,
			objs:    []runtime.Object{cp1, cp2},
			etcdClientGenerator: &fakeEtcdClientGenerator{
				client: &etcd.Client{
					EtcdClient: &fake2.FakeEtcdClient{
						MemberListResponse: &clientv3.MemberListResponse{
							Members: []*pb.Member{
								{Name: "cp1", ID: uint64(1)},
								{Name: "test-2", ID: uint64(2)},
								{Name: "test-3", ID: uint64(3)},
							},
						},
						AlarmResponse: &clientv3.AlarmResponse{
							Alarms: []*pb.AlarmMember{},
						},
					},
				},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			fakeClient := fake.NewFakeClientWithScheme(scheme, tt.objs...)
			w := &Workload{
				Client:              fakeClient,
				etcdClientGenerator: tt.etcdClientGenerator,
			}
			ctx := context.TODO()
			err := w.RemoveEtcdMemberForMachine(ctx, tt.machine)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
		})
	}
}

func TestForwardEtcdLeadership(t *testing.T) {
	t.Run("handles errors correctly", func(t *testing.T) {
		machine := &clusterv1.Machine{
			Status: clusterv1.MachineStatus{
				NodeRef: &corev1.ObjectReference{
					Name: "machine-node",
				},
			},
		}
		machineNoNode := machine.DeepCopy()
		machineNoNode.Status.NodeRef.Name = "does-not-exist"
		tests := []struct {
			name                string
			machine             *clusterv1.Machine
			leaderCandidate     *clusterv1.Machine
			etcdClientGenerator etcdClientFor
			expectErr           bool
		}{
			{
				name:      "does not panic if machine is nil",
				expectErr: false,
			},
			{
				name: "does not panic if machine noderef is nil",
				machine: &clusterv1.Machine{
					Status: clusterv1.MachineStatus{
						NodeRef: nil,
					},
				},
				expectErr: false,
			},
			{
				name:                "returns error if cannot find etcdClient for node",
				machine:             machineNoNode,
				etcdClientGenerator: &fakeEtcdClientGenerator{err: errors.New("no etcdClient")},
				expectErr:           true,
			},
			{
				name:    "returns error if it failed to get etcd members",
				machine: machine,
				etcdClientGenerator: &fakeEtcdClientGenerator{
					client: &etcd.Client{
						EtcdClient: &fake2.FakeEtcdClient{
							ErrorResponse: errors.New("cannot get etcd members"),
						},
					},
				},
				expectErr: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				g := NewWithT(t)
				w := &Workload{
					etcdClientGenerator: tt.etcdClientGenerator,
				}
				ctx := context.TODO()
				err := w.ForwardEtcdLeadership(ctx, tt.machine, tt.leaderCandidate)
				if tt.expectErr {
					g.Expect(err).To(HaveOccurred())
					return
				}
				g.Expect(err).ToNot(HaveOccurred())
			})
		}
	})

	t.Run("does noop if machine etcd member ID does not match etcdClient leader ID", func(t *testing.T) {
		g := NewWithT(t)
		machine := &clusterv1.Machine{
			Status: clusterv1.MachineStatus{
				NodeRef: &corev1.ObjectReference{
					Name: "machine-node",
				},
			},
		}
		fakeEtcdClient := &fake2.FakeEtcdClient{
			MemberListResponse: &clientv3.MemberListResponse{
				Members: []*pb.Member{
					{Name: "machine-node", ID: uint64(101)},
					{Name: "other-node", ID: uint64(1034)},
				},
			},
			AlarmResponse: &clientv3.AlarmResponse{
				Alarms: []*pb.AlarmMember{},
			},
		}
		etcdClientGenerator := &fakeEtcdClientGenerator{
			client: &etcd.Client{
				EtcdClient: fakeEtcdClient,
				// this etcd client does not belong to the current
				// machine. Ideally, this would match 101 from members
				// list
				LeaderID: 555,
			},
		}

		w := &Workload{
			etcdClientGenerator: etcdClientGenerator,
		}
		ctx := context.TODO()
		err := w.ForwardEtcdLeadership(ctx, machine, nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(fakeEtcdClient.MovedLeader).To(BeEquivalentTo(0))

	})

	t.Run("move etcd leader", func(t *testing.T) {
		machine := &clusterv1.Machine{
			Status: clusterv1.MachineStatus{
				NodeRef: &corev1.ObjectReference{
					Name: "machine-node",
				},
			},
		}
		leaderCandidate := &clusterv1.Machine{
			Status: clusterv1.MachineStatus{
				NodeRef: &corev1.ObjectReference{
					Name: "leader-node",
				},
			},
		}
		leaderCandidateBadNodeRef := leaderCandidate.DeepCopy()
		leaderCandidateBadNodeRef.Status.NodeRef.Name = "does-not-exist"
		tests := []struct {
			name               string
			leaderCandidate    *clusterv1.Machine
			etcdMoveErr        error
			expectedMoveLeader uint64
			expectErr          bool
		}{
			{
				name:               "to the next available member",
				expectedMoveLeader: 1034,
			},
			{
				name:        "returns error if failed to move to the next available member",
				etcdMoveErr: errors.New("move err"),
				expectErr:   true,
			},
			{
				name:               "to the leader candidate",
				leaderCandidate:    leaderCandidate,
				expectedMoveLeader: 12345,
			},
			{
				name:            "returns error if failed to move to the leader candidate",
				leaderCandidate: leaderCandidate,
				etcdMoveErr:     errors.New("move err"),
				expectErr:       true,
			},
			{
				name:            "returns error if it cannot find the leader etcd member",
				leaderCandidate: leaderCandidateBadNodeRef,
				expectErr:       true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				g := NewWithT(t)
				fakeEtcdClient := &fake2.FakeEtcdClient{
					ErrorResponse: tt.etcdMoveErr,
					MemberListResponse: &clientv3.MemberListResponse{
						Members: []*pb.Member{
							{Name: "machine-node", ID: uint64(101)},
							{Name: "other-node", ID: uint64(1034)},
							{Name: "leader-node", ID: uint64(12345)},
						},
					},
					AlarmResponse: &clientv3.AlarmResponse{
						Alarms: []*pb.AlarmMember{},
					},
				}

				etcdClientGenerator := &fakeEtcdClientGenerator{
					client: &etcd.Client{
						EtcdClient: fakeEtcdClient,
						// this etcdClient belongs to the machine-node
						LeaderID: 101,
					},
				}

				w := &Workload{
					etcdClientGenerator: etcdClientGenerator,
				}
				ctx := context.TODO()
				err := w.ForwardEtcdLeadership(ctx, machine, tt.leaderCandidate)
				if tt.expectErr {
					g.Expect(err).To(HaveOccurred())
					return
				}
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(fakeEtcdClient.MovedLeader).To(BeEquivalentTo(tt.expectedMoveLeader))
			})
		}
	})
}

type fakeEtcdClientGenerator struct {
	client *etcd.Client
	err    error
}

func (c *fakeEtcdClientGenerator) forNode(_ context.Context, _ string) (*etcd.Client, error) {
	return c.client, c.err
}

type podOption func(*corev1.Pod)

func etcdPod(name string, options ...podOption) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceSystem,
		},
	}
	for _, opt := range options {
		opt(p)
	}
	return p
}
func withReadyOption(pod *corev1.Pod) {
	readyCondition := corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}
	pod.Status.Conditions = append(pod.Status.Conditions, readyCondition)
}

func withProviderID(pi string) func(corev1.Node) corev1.Node {
	return func(node corev1.Node) corev1.Node {
		node.Spec.ProviderID = pi
		return node
	}
}
