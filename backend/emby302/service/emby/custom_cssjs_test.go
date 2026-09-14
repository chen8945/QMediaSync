package emby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestProxyCustomJsWaitsForEmby(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 Node.js 执行自定义脚本行为测试")
	}

	originalLoader, originalScripts := loadAllCustomCssJs, customJsList
	loadAllCustomCssJs = func() {}
	customJsList = []string{
		`events.push("first"); throw new Error("expected script failure");`,
		`"use strict";
var ApiClient = "local";
events.push(this === undefined ? "second" : "wrong scope");`,
	}
	t.Cleanup(func() {
		loadAllCustomCssJs, customJsList = originalLoader, originalScripts
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/custom.js", nil)
	ProxyCustomJs(ctx)

	const checkScript = `
const assert = require('node:assert/strict');
const source = require('node:fs').readFileSync(0, 'utf8');
const vm = require('node:vm');
const state = process.argv[1];
const timers = [];
const events = [];
const errors = [];
const sandbox = {
  events,
  console: { error: (...args) => errors.push(args) },
  setTimeout(callback, delay) {
    assert.equal(delay, 100);
    timers.push(callback);
  }
};
if (state !== 'undefined') sandbox.ApiClient = state === 'null' ? null : {};
vm.runInNewContext(source, sandbox, { timeout: 1000 });
if (state !== 'ready') {
  assert.deepEqual(events, [], 'scripts must wait for ApiClient');
  assert.equal(errors.length, 0);
  assert.equal(timers.length, 2);
  timers.splice(0).forEach(callback => callback());
  assert.deepEqual(events, [], 'polling must keep waiting while ApiClient is unavailable');
  assert.equal(timers.length, 2);
  sandbox.ApiClient = {};
  timers.splice(0).forEach(callback => callback());
}
assert.deepEqual(events, ['first', 'second'], 'each script must execute once in its own scope');
assert.equal(errors.length, 1, 'one script failure must not prevent another script');
assert.equal(errors[0][1].message, 'expected script failure');
assert.equal(timers.length, 0, 'ready scripts must stop polling');
`
	for _, state := range []string{"undefined", "null", "ready"} {
		t.Run(state, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(
				ctx,
				node,
				"-e",
				checkScript,
				state,
			)
			cmd.Stdin = strings.NewReader(recorder.Body.String())
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("自定义脚本行为验证失败: %v\n%s", err, output)
			}
		})
	}
}
