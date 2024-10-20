/*
Copyright 2019 The Kubernetes Authors.

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

package cloud

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"os"

	"github.com/golang/mock/gomock"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8s_testing "k8s.io/client-go/testing"
	"sigs.k8s.io/aws-fsx-csi-driver/pkg/cloud/mocks"
	"testing"
)

const (
	nodeName            = "ip-123-45-67-890.us-west-2.compute.internal"
	stdInstanceID       = "i-abcdefgh123456789"
	stdInstanceType     = "t2.medium"
	stdRegion           = "us-west-2"
	stdAvailabilityZone = "us-west-2b"
)

func TestNewMetadataService(t *testing.T) {
	testCases := []struct {
		name                             string
		ec2metadataAvailable             bool
		clientsetReactors                func(*fake.Clientset)
		getInstanceIdentityDocumentValue imds.InstanceIdentityDocument
		getInstanceIdentityDocumentError error
		invalidInstanceIdentityDocument  bool
		expectedErr                      error
		node                             v1.Node
		nodeNameEnvVar                   string
	}{
		{
			name:                 "success: normal",
			ec2metadataAvailable: true,
			getInstanceIdentityDocumentValue: imds.InstanceIdentityDocument{
				InstanceID:       stdInstanceID,
				InstanceType:     stdInstanceType,
				Region:           stdRegion,
				AvailabilityZone: stdAvailabilityZone,
			},
			expectedErr: nil,
		},
		// TODO: Once topology is implemented, add test cases for kubernetes metadata
		{
			name:                 "failure: metadata not available, k8s client error",
			ec2metadataAvailable: false,
			clientsetReactors: func(clientset *fake.Clientset) {
				clientset.PrependReactor("get", "*", func(action k8s_testing.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, fmt.Errorf("client failure")
				})
			},
			expectedErr:    fmt.Errorf("error getting Node %s: client failure", nodeName),
			nodeNameEnvVar: nodeName,
		},
		{
			name:                 "failure: metadata not available, node name env var not set",
			ec2metadataAvailable: false,
			expectedErr:          fmt.Errorf("CSI_NODE_NAME env var not set"),
			nodeNameEnvVar:       "",
		},
		{
			name:                             "fail: GetInstanceIdentityDocument returned error",
			ec2metadataAvailable:             true,
			getInstanceIdentityDocumentError: fmt.Errorf("foo"),
			expectedErr:                      fmt.Errorf("could not get EC2 instance identity metadata: foo"),
		},
		{
			name:                 "fail: GetInstanceIdentityDocument returned empty instance",
			ec2metadataAvailable: true,
			getInstanceIdentityDocumentValue: imds.InstanceIdentityDocument{
				InstanceID:       "",
				InstanceType:     stdInstanceType,
				Region:           stdRegion,
				AvailabilityZone: stdAvailabilityZone,
			},
			invalidInstanceIdentityDocument: true,
			expectedErr:                     fmt.Errorf("could not get valid EC2 instance ID"),
		},
		{
			name:                 "fail: GetInstanceIdentityDocument returned empty az",
			ec2metadataAvailable: true,
			getInstanceIdentityDocumentValue: imds.InstanceIdentityDocument{
				InstanceID:       stdInstanceID,
				InstanceType:     stdInstanceType,
				Region:           stdRegion,
				AvailabilityZone: "",
			},
			invalidInstanceIdentityDocument: true,
			expectedErr:                     fmt.Errorf("could not get valid EC2 availability zone"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset(&tc.node)
			clientsetInitialized := false
			if tc.clientsetReactors != nil {
				tc.clientsetReactors(clientset)
			}

			mockCtrl := gomock.NewController(t)
			mockEC2Metadata := mocks.NewMockEC2Metadata(mockCtrl)

			ec2MetadataClient := func() (EC2Metadata, error) { return mockEC2Metadata, nil }
			k8sAPIClient := func() (kubernetes.Interface, error) { clientsetInitialized = true; return clientset, nil }

			if tc.ec2metadataAvailable {
				mockEC2Metadata.EXPECT().GetMetadata(context.TODO(), &imds.GetMetadataInput{Path: "instance-id"}).Return(nil, nil)
				mockEC2Metadata.EXPECT().GetInstanceIdentityDocument(context.TODO(), &imds.GetInstanceIdentityDocumentInput{}).Return(&imds.GetInstanceIdentityDocumentOutput{InstanceIdentityDocument: tc.getInstanceIdentityDocumentValue}, tc.getInstanceIdentityDocumentError)

				if clientsetInitialized == true {
					t.Errorf("kubernetes client was unexpectedly initialized when metadata is available!")
					if len(clientset.Actions()) > 0 {
						t.Errorf("kubernetes client was unexpectedly called! %v", clientset.Actions())
					}
				}
			} else {
				mockEC2Metadata.EXPECT().GetMetadata(context.TODO(), &imds.GetMetadataInput{Path: "instance-id"}).Return(nil, errors.New("fake error"))
			}

			os.Setenv("CSI_NODE_NAME", tc.nodeNameEnvVar)
			var m MetadataService
			var err error
			m, err = NewMetadataService(ec2MetadataClient, k8sAPIClient, stdRegion)

			if err != nil {
				if tc.expectedErr == nil {
					t.Errorf("got error %q, expected no error", err)
				} else if err.Error() != tc.expectedErr.Error() {
					t.Errorf("got error %q, expected %q", err, tc.expectedErr)
				}
			} else {
				if m == nil {
					t.Fatalf("metadataService is unexpectedly nil!")
				}
				if m.GetInstanceID() != stdInstanceID {
					t.Errorf("NewMetadataService() failed: got wrong instance ID %v, expected %v", m.GetInstanceID(), stdInstanceID)
				}
				if m.GetInstanceType() != stdInstanceType {
					t.Errorf("GetInstanceType() failed: got wrong instance type %v, expected %v", m.GetInstanceType(), stdInstanceType)
				}
				if m.GetRegion() != stdRegion {
					t.Errorf("NewMetadataService() failed: got wrong region %v, expected %v", m.GetRegion(), stdRegion)
				}
				if m.GetAvailabilityZone() != stdAvailabilityZone {
					t.Errorf("NewMetadataService() failed: got wrong AZ %v, expected %v", m.GetAvailabilityZone(), stdAvailabilityZone)
				}
			}
			mockCtrl.Finish()
		})
	}
}
