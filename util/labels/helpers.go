/*
Copyright 2021 The Kubernetes Authors.

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

// Package labels implements label utility functions.
package labels

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// IsTopologyOwned returns true if the object has the `topology.cluster.x-k8s.io/owned` label.
func IsTopologyOwned(o metav1.Object) bool {
	_, ok := o.GetLabels()[clusterv1.ClusterTopologyOwnedLabel]
	return ok
}

// HasWatchLabel returns true if the object has a label with the WatchLabel key matching the given value.
func HasWatchLabel(o metav1.Object, labelValue string) bool {
	val, ok := o.GetLabels()[clusterv1.WatchLabel]
	if !ok {
		return false
	}
	return val == labelValue
}

// MustFormatValue returns the passed inputLabelValue if it meets the standards for a Kubernetes label value.
// If the name is not a valid label value this function returns a hash which meets the requirements.
func MustFormatValue(str string) string {
	// a valid Kubernetes label value must:
	// - be less than 64 characters long.
	// - be an empty string OR consist of alphanumeric characters, '-', '_' or '.'.
	// - start and end with an alphanumeric character
	if len(validation.IsValidLabelValue(str)) == 0 {
		return str
	}
	hasher := fnv.New32a()
	_, err := hasher.Write([]byte(str))
	if err != nil {
		// At time of writing the implementation of fnv's Write function can never return an error.
		// If this changes in a future go version this function will panic.
		panic(err)
	}
	return fmt.Sprintf("hash_%s_z", base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hasher.Sum(nil)))
}

// MustEqualValue returns true if the actualLabelValue equals either the inputLabelValue or the hashed
// value of the inputLabelValue.
func MustEqualValue(str, labelValue string) bool {
	return labelValue == MustFormatValue(str)
}
