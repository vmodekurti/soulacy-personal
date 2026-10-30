# Enforcing output contracts

A model can omit a required field even when its prose says the report is complete.
Declare required fields in code and validate the saved artifact independently.

## Python tool inputs

The runtime now validates an agent's Python tool `parameters` JSON Schema before
starting Python. Invalid calls return a tool error, including the failing schema
path, so the model can repair the arguments. This applies to inline and file-based
Python tools dispatched by the runtime, including workflow and reasoning calls.
A rejected call cannot execute the Python side effect. Tools without a schema
retain their existing behavior. Built-in and MCP handlers retain their own input
validation; this change does not add a new validator to those handlers.

Use `required` inside nested objects as well as at the top level. Conditional
schemas can require `report` for a save operation without requiring it for reads:

```yaml
parameters:
  type: object
  required: [op]
  properties:
    op: {type: string, enum: [read, save]}
    report:
      type: object
      required: [error_rate_multiplier]
      properties:
        error_rate_multiplier: {type: number, minimum: 0}
  if:
    properties:
      op: {const: save}
  then:
    required: [report]
```

Schemas must be self-contained. Local `$ref` definitions work; fetching external
schemas from disk or the network is disabled. An invalid schema produces an
error, not an unchecked tool invocation.

## Structured agent answers

For classic and reasoning agent loops, `llm.output_schema` is checked locally
against the final JSON value, including required fields and value types. One
corrective model call is allowed. Its result is validated again. If it still
fails, the run returns an error before persisting or delivering a successful
assistant answer. Native provider JSON mode alone is not sufficient.

Valid structured answers are not unwrapped as prose, combined with chart fences,
or transformed for voice delivery. Declare any desired fields in the schema.
Workflow outcome contracts remain the mechanism for checking workflow results.

## Independent business checks

Schema validation checks shape, not truth. The example in
`examples/python/incident-report/` additionally recomputes the synthetic incident
metrics, deduplicated credits, action constraints, and optimal plan. The policy
thresholds and rollback restriction in that example are specific to its fixture.
Run the checker before saving and on the actual readback. A receipt should name
its scope and identify the artifact it checked (for example, with a SHA-256).
The example checks prose presence, not every assertion in the prose.

## CLI response metadata

`sy chat --json` prints the full gateway JSON response, preserving the session ID
and any metadata returned by the server. Without `--json`, it prints reply text.

## Regression checks

```sh
go test ./internal/jsoncontract ./internal/runtime ./cmd/sy
python3 -m unittest discover -s examples/python/incident-report -v
```
