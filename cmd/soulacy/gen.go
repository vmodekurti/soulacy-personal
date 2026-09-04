package main

// Regenerates builtins_gen.go — the blank-import file that links built-in
// driver registrations into the binary (Story E10).
//go:generate go run ../../scripts/genbuiltins -out builtins_gen.go
