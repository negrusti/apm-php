// Relay modifications, 2026. SPDX-License-Identifier: Apache-2.0
package newrelic

import "testing"

func TestRelayDisablesInfiniteTracing(t *testing.T) {
	p := &Processor{cfg: ProcessorConfig{DisableInfiniteTracing: true}, appInfoChannel: make(chan AppInfoMessage)}
	original := &AppInfo{TraceObserverHost: "observer.invalid", TraceObserverPort: 443, Appname: "WordPress"}
	done := make(chan *AppInfo)
	go func() { m := <-p.appInfoChannel; m.ResultChan <- AppInfoReply{}; done <- m.Info }()
	p.IncomingAppInfo(nil, original)
	seen := <-done
	if seen.TraceObserverHost != "" || seen.TraceObserverPort != 0 || seen.Appname != original.Appname {
		t.Fatalf("observer not disabled: %+v", seen)
	}
	if original.TraceObserverHost != "observer.invalid" {
		t.Fatal("modified agent's original message")
	}
}
