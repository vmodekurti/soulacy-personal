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
	"write_file":      {Privileged: true, SideEffecting: true},
	"download_file":   {Privileged: true, SideEffecting: true},
}
