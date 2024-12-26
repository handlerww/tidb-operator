// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tiflashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTiFlashControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/tiflash/store-status", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, err := w.Write([]byte("Ready"))
		assert.NoError(t, err)
	}))
	defer server.Close()

	// only test non-tls
	client, err := NewDefaultTiFlashControl().GetTiFlashPodClient(context.Background(), nil, "", "", "", false)
	require.NoError(t, err)
	client.(*tiflashClient).url = server.URL
	status, err := client.GetStoreStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Ready, status)
}
