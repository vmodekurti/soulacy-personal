// Package mcppackage builds safe commands for exact-version MCP registry
// packages that execute only inside Soulacy's container transport.
package mcppackage

import (
	"regexp"
	"strings"
)

var (
	safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9@._/-]{1,220}$`)
	safeVersion    = regexp.MustCompile(`^[0-9][0-9A-Za-z._+-]{0,80}$`)
)

// nodeBinRunner resolves the exact package's declared bin entry and invokes it
// through node. npm's generated .bin shim can legitimately lack an executable
// bit in a published package; direct node execution works without weakening the
// read-only, non-root container or using a shell.
const nodeBinRunner = `const fs=require("fs"),p=require("path"),cp=require("child_process");const id=process.argv[1],args=process.argv.slice(2);const binDir=(process.env.PATH||"").split(p.delimiter).find(v=>v.endsWith("/node_modules/.bin"));if(!binDir)throw new Error("npm package bin directory is unavailable");const modules=p.dirname(binDir),root=p.resolve(modules,id),manifest=JSON.parse(fs.readFileSync(p.join(root,"package.json"),"utf8"));const bins=manifest.bin||{};let rel=typeof bins==="string"?bins:bins[String(manifest.name||id).split("/").pop()]||Object.values(bins).sort()[0];if(!rel)throw new Error("MCP package declares no executable");const realRoot=fs.realpathSync(root)+p.sep,entry=fs.realpathSync(p.resolve(root,rel));if(!entry.startsWith(realRoot))throw new Error("MCP package executable escapes its package root");const child=cp.spawn(process.execPath,[entry,...args],{stdio:"inherit"});for(const sig of ["SIGINT","SIGTERM","SIGHUP"])process.on(sig,()=>child.kill(sig));child.on("error",e=>{console.error(e.message);process.exit(126)});child.on("exit",(code,signal)=>{if(signal)process.kill(process.pid,signal);else process.exit(code??1)});`

// NodeRunnerArgs returns a shell-free npm command for one immutable package
// version. The caller validates the identifier and version before approval.
func NodeRunnerArgs(identifier, version string, runtimeArgs []string) []string {
	spec := identifier + "@" + version
	out := []string{"npm", "exec", "--yes", "--ignore-scripts", "--package", spec, "--", "node", "-e", nodeBinRunner, identifier}
	return append(out, runtimeArgs...)
}

// UpgradeLegacyNodeRunnerArgs transparently repairs definitions created by
// older Soulacy releases. It recognizes only the exact closed command shape
// Soulacy itself emitted and leaves all other tenant commands untouched.
func UpgradeLegacyNodeRunnerArgs(args []string) []string {
	if len(args) < 4 || args[0] != "npx" || args[1] != "--yes" || args[2] != "--ignore-scripts" {
		return args
	}
	spec := args[3]
	i := strings.LastIndex(spec, "@")
	if i <= 0 {
		return args
	}
	identifier, version := spec[:i], spec[i+1:]
	if !safeIdentifier.MatchString(identifier) || strings.Contains(identifier, "..") || !safeVersion.MatchString(version) {
		return args
	}
	return NodeRunnerArgs(identifier, version, args[4:])
}
