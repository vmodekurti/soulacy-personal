package runtime

// toolSecurityClass is the single authoritative classification record for a
// host builtin. Keeping both properties in one map prevents a tool name from
// appearing in two distant security maps with divergent answers.
type toolSecurityClass struct {
	Privileged    bool
	SideEffecting bool
}

var toolSecurityClasses = map[string]toolSecurityClass{
	"env_get":         {},
	"sys_info":        {},
	"read_file":       {},
	"list_dir":        {},
	"find_files":      {},
	"fetch_url":       {},
	"http_request":    {SideEffecting: true},
	"shell_exec":      {Privileged: true, SideEffecting: true},
	"run_script":      {Privileged: true, SideEffecting: true},
	"python_eval":     {Privileged: true, SideEffecting: true},
	"install_library": {Privileged: true, SideEffecting: true},
	"package_install": {Privileged: true, SideEffecting: true},
	"write_file":      {Privileged: true, SideEffecting: true},
	"download_file":   {Privileged: true, SideEffecting: true},
}

// requiresPrivilegedIsolation distinguishes unrestricted host execution from
// the managed URL installer. package_install remains privileged for RBAC,
// intent checks, confirmation, audit, and policy classification, but its
// implementation executes only a fixed argv through the hardened installer.
// Running it in the ordinary no-network sandbox would make installation
// impossible and would prevent it from updating the persistent workspace.
func requiresPrivilegedIsolation(name string) bool {
	return isPrivilegedSystemTool(name) && name != "package_install"
}
