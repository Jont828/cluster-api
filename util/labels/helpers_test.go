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

package labels

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func TestHasWatchLabel(t *testing.T) {
	g := NewWithT(t)

	var testcases = []struct {
		name     string
		obj      metav1.Object
		input    string
		expected bool
	}{
		{
			name: "should handle empty input",
			obj: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
				Spec:   corev1.NodeSpec{},
				Status: corev1.NodeStatus{},
			},
			input:    "",
			expected: false,
		},
		{
			name: "should return false if no input string is give",
			obj: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						clusterv1.WatchLabel: "bar",
					},
				},
			},
			input:    "",
			expected: false,
		},
		{
			name: "should return true if label matches",
			obj: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						clusterv1.WatchLabel: "bar",
					},
				},
				Spec:   corev1.NodeSpec{},
				Status: corev1.NodeStatus{},
			},
			input:    "bar",
			expected: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			res := HasWatchLabel(tc.obj, tc.input)
			g.Expect(res).To(Equal(tc.expected))
		})
	}
}

func TestNameLabelValue(t *testing.T) {
	g := NewWithT(t)
	tests := []struct {
		name           string
		machineSetName string
		want           string
	}{
		{
			name:           "return the name if it's less than 63 characters",
			machineSetName: "machineSetName",
			want:           "machineSetName",
		},
		{
			name:           "return  for a name with more than 63 characters",
			machineSetName: "machineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetName",
			want:           "hash_FR_ghQ_z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MustFormatValue(tt.machineSetName)
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

func TestMustMatchLabelValueForName(t *testing.T) {
	g := NewWithT(t)
	tests := []struct {
		name           string
		machineSetName string
		labelValue     string
		want           bool
	}{
		{
			name:           "match labels when MachineSet name is short",
			machineSetName: "ms1",
			labelValue:     "ms1",
			want:           true,
		},
		{
			name:           "don't match different labels when MachineSet name is short",
			machineSetName: "ms1",
			labelValue:     "notMS1",
			want:           false,
		},
		{
			name:           "don't match labels when MachineSet name is long",
			machineSetName: "machineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetName",
			labelValue:     "hash_Nx4RdE_z",
			want:           false,
		},
		{
			name:           "match labels when MachineSet name is long",
			machineSetName: "machineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetNamemachineSetName",
			labelValue:     "hash_FR_ghQ_z",
			want:           true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MustEqualValue(tt.machineSetName, tt.labelValue)
			g.Expect(got).To(Equal(tt.want))
		})
	}
}
