// +build integ
// Copyright Istio Authors. All Rights Reserved.
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

package policy

import (
	"io/ioutil"
	"testing"
	"time"

	"istio.io/istio/pkg/config/protocol"
	"istio.io/istio/pkg/test/echo/common/response"
	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/echo"
	"istio.io/istio/pkg/test/framework/components/echo/echoboot"
	"istio.io/istio/pkg/test/framework/components/istio"
	"istio.io/istio/pkg/test/framework/components/istio/ingress"
	"istio.io/istio/pkg/test/framework/components/namespace"
	"istio.io/istio/pkg/test/framework/label"
	"istio.io/istio/pkg/test/framework/resource"
	"istio.io/istio/pkg/test/kube"
	"istio.io/istio/pkg/test/util/tmpl"
)

var (
	ist         istio.Instance
	echoNsInst  namespace.Instance
	ratelimitNs namespace.Instance
	ing         ingress.Instance
	srv         echo.Instance
	clt         echo.Instance
)

func TestRateLimiting(t *testing.T) {
	framework.
		NewTest(t).
		Features("traffic.ratelimit.envoy").
		Run(func(ctx framework.TestContext) {
			isLocal := false
			yaml, err := setupEnvoyFilter(ctx, isLocal)
			if err != nil {
				t.Fatalf("Could not setup envoy filter patches.")
			}
			defer cleanupEnvoyFilter(ctx, yaml)

			if !sendTrafficAndCheckIfRatelimited(t) {
				t.Errorf("No request received StatusTooMantRequest Error.")
			}
		})
}

func TestLocalRateLimiting(t *testing.T) {
	framework.
		NewTest(t).
		Features("traffic.ratelimit.envoy").
		Run(func(ctx framework.TestContext) {
			isLocal := true
			yaml, err := setupEnvoyFilter(ctx, isLocal)
			if err != nil {
				t.Fatalf("Could not setup envoy filter patches.")
			}
			defer cleanupEnvoyFilter(ctx, yaml)

			if !sendTrafficAndCheckIfRatelimited(t) {
				t.Errorf("No request received StatusTooMantRequest Error.")
			}
		})
}

func TestMain(m *testing.M) {
	framework.
		NewSuite(m).
		RequireSingleCluster().
		Label(label.CustomSetup).
		Setup(istio.Setup(&ist, nil)).
		Setup(testSetup).
		Run()
}

func testSetup(ctx resource.Context) (err error) {
	echoNsInst, err = namespace.New(ctx, namespace.Config{
		Prefix: "istio-echo",
		Inject: true,
	})
	if err != nil {
		return
	}

	_, err = echoboot.NewBuilder(ctx).
		With(&clt, echo.Config{
			Service:   "clt",
			Namespace: echoNsInst}).
		With(&srv, echo.Config{
			Service:   "srv",
			Namespace: echoNsInst,
			Ports: []echo.Port{
				{
					Name:     "http",
					Protocol: protocol.HTTP,
					// We use a port > 1024 to not require root
					InstancePort: 8888,
				},
			}}).
		Build()
	if err != nil {
		return
	}

	ing = ist.IngressFor(ctx.Clusters().Default())

	ratelimitNs, err = namespace.New(ctx, namespace.Config{
		Prefix: "istio-ratelimit",
	})
	if err != nil {
		return
	}

	yamlContent, err := ioutil.ReadFile("testdata/ratelimitservice.yaml")
	if err != nil {
		return
	}

	err = ctx.Config().ApplyYAML(ratelimitNs.Name(),
		string(yamlContent),
	)
	if err != nil {
		return
	}

	// Wait for redis and ratelimit service to be up.
	fetchFn := kube.NewPodFetch(ctx.Clusters().Default(), ratelimitNs.Name(), "app=redis")
	if _, err = kube.WaitUntilPodsAreReady(fetchFn); err != nil {
		return
	}
	fetchFn = kube.NewPodFetch(ctx.Clusters().Default(), ratelimitNs.Name(), "app=ratelimit")
	if _, err = kube.WaitUntilPodsAreReady(fetchFn); err != nil {
		return
	}

	// TODO(gargnupur): Figure out a way to query, envoy is ready to talk to rate limit service.
	// Also, change to use mock rate limit and redis service.
	time.Sleep(time.Second * 60)

	return nil
}

func setupEnvoyFilter(ctx resource.Context, isLocal bool) (string, error) {
	file := "testdata/enable_envoy_ratelimit.yaml"
	if isLocal {
		file = "testdata/enable_envoy_local_ratelimit.yaml"
	}
	content, err := ioutil.ReadFile(file)
	if err != nil {
		return "", err
	}

	con, err := tmpl.Evaluate(string(content), map[string]interface{}{
		"EchoNamespace":      echoNsInst.Name(),
		"RateLimitNamespace": ratelimitNs.Name(),
	})
	if err != nil {
		return "", err
	}

	err = ctx.Config().ApplyYAML(ist.Settings().SystemNamespace, con)
	if err != nil {
		return "", err
	}
	return con, nil
}

func cleanupEnvoyFilter(ctx resource.Context, yaml string) error {
	err := ctx.Config().DeleteYAML(ist.Settings().SystemNamespace, yaml)
	if err != nil {
		return err
	}
	return nil
}

func sendTrafficAndCheckIfRatelimited(t *testing.T) bool {
	t.Helper()
	t.Logf("Sending 300 requests...")
	httpOpts := echo.CallOptions{
		Target:   srv,
		PortName: "http",
		Count:    300,
	}
	if parsedResponse, err := clt.Call(httpOpts); err == nil {
		for _, resp := range parsedResponse {
			if response.StatusCodeTooManyRequests == resp.Code {
				return true
			}
		}
	}
	return false
}
