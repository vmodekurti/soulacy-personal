package autopilot

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

const maxMicrosCostUSD = float64(math.MaxInt64) / 1_000_000

// ValidateMission checks a MissionContract before it is saved or deployed.
// Evaluation remains fail-safe even if validation was skipped: malformed and
// unknown checks evaluate to unknown, never pass.
func ValidateMission(contract *agent.MissionContract) error {
	if contract == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(contract.Acceptance))
	for i, check := range contract.Acceptance {
		if id := strings.TrimSpace(check.ID); id != "" {
			if id == "limit-max-cost" || id == "limit-max-duration" || id == "limit-allowed-tools" {
				return fmt.Errorf("%w: mission check id %q is reserved", ErrInvalid, id)
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("%w: mission check id %q is duplicated", ErrInvalid, id)
			}
			seen[id] = struct{}{}
		}
		if err := validateMissionCheck(check); err != nil {
			return fmt.Errorf("%w: mission acceptance[%d]: %v", ErrInvalid, i, err)
		}
	}
	return validateMissionLimits(contract.Limits)
}

func validateMissionLimits(limits agent.MissionLimits) error {
	if limits.MaxCostUSD != nil && (*limits.MaxCostUSD < 0 || *limits.MaxCostUSD > maxMicrosCostUSD ||
		math.IsNaN(*limits.MaxCostUSD) || math.IsInf(*limits.MaxCostUSD, 0)) {
		return fmt.Errorf("%w: mission limits max_cost_usd must be finite, non-negative, and representable in micro-dollars", ErrInvalid)
	}
	if strings.TrimSpace(limits.MaxDuration) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(limits.MaxDuration))
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("%w: mission limits max_duration must be positive and no more than 24h", ErrInvalid)
		}
	}
	if limits.AllowedTools != nil {
		allowed := *limits.AllowedTools
		if len(allowed) > 128 {
			return fmt.Errorf("%w: mission limits allowed_tools cannot exceed 128 entries", ErrInvalid)
		}
		seen := make(map[string]struct{}, len(allowed))
		for i, name := range allowed {
			trimmed := strings.TrimSpace(name)
			if name != trimmed {
				return fmt.Errorf("%w: mission limits allowed_tools[%d] has surrounding whitespace", ErrInvalid, i)
			}
			name = trimmed
			if name == "" {
				return fmt.Errorf("%w: mission limits allowed_tools[%d] is blank", ErrInvalid, i)
			}
			if name == "*" || strings.EqualFold(name, "all") {
				return fmt.Errorf("%w: mission limits allowed_tools does not permit wildcards", ErrInvalid)
			}
			if len(name) > 512 {
				return fmt.Errorf("%w: mission limits allowed_tools[%d] exceeds 512 bytes", ErrInvalid, i)
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("%w: mission limits allowed_tools contains duplicate %q", ErrInvalid, name)
			}
			seen[name] = struct{}{}
		}
	}
	return nil
}

func validateMissionCheck(check agent.MissionCheck) error {
	switch check.Type {
	case agent.MissionCheckOutputContains, agent.MissionCheckOutputNotContains:
		if check.Value == "" {
			return fmt.Errorf("%s requires value", check.Type)
		}
	case agent.MissionCheckOutputRegex:
		if check.Value == "" {
			return fmt.Errorf("output_regex requires value")
		}
		if _, err := regexp.Compile(check.Value); err != nil {
			return fmt.Errorf("invalid output_regex: %w", err)
		}
	case agent.MissionCheckRequiredTool:
		if strings.TrimSpace(check.Tool) == "" {
			return fmt.Errorf("required_tool requires tool")
		}
	case agent.MissionCheckMaxDuration:
		d, err := time.ParseDuration(strings.TrimSpace(check.Duration))
		if err != nil || d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("max_duration requires a positive Go duration no greater than 24h")
		}
	case agent.MissionCheckMaxCost:
		if check.CostUSD == nil || *check.CostUSD < 0 || *check.CostUSD > maxMicrosCostUSD ||
			math.IsNaN(*check.CostUSD) || math.IsInf(*check.CostUSD, 0) {
			return fmt.Errorf("max_cost requires a finite, non-negative cost_usd representable in micro-dollars")
		}
	case agent.MissionCheckClaim:
		if strings.TrimSpace(check.Claim) == "" {
			return fmt.Errorf("business_outcome requires claim")
		}
	default:
		return fmt.Errorf("unknown check type %q", check.Type)
	}
	return nil
}

// EvaluateMission applies deterministic checks to trusted run observations.
// The overall result is fail if any check fails, pass if every check passes,
// and unknown otherwise (including an empty contract or an unmeasured metric).
func EvaluateMission(contract *agent.MissionContract, observation Observation) MissionEvaluation {
	if contract == nil || !contract.HasChecks() {
		return MissionEvaluation{Verification: CheckUnknown, Checks: []CheckResult{}}
	}

	results := make([]CheckResult, 0, len(contract.Acceptance)+3)
	for i, check := range contract.Acceptance {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			id = fmt.Sprintf("check-%d", i+1)
		}
		result := CheckResult{
			ID:          id,
			Type:        check.Type,
			Description: check.Description,
			Status:      CheckUnknown,
		}
		if err := validateMissionCheck(check); err != nil {
			result.Detail = err.Error()
			results = append(results, result)
			continue
		}

		switch check.Type {
		case agent.MissionCheckOutputContains:
			result.Expected = check.Value
			if strings.Contains(observation.Output, check.Value) {
				result.Status = CheckPass
				result.Actual = "present"
			} else {
				result.Status = CheckFail
				result.Actual = "absent"
				result.Detail = "final output did not contain the required text"
			}
		case agent.MissionCheckOutputNotContains:
			result.Expected = "output excludes " + strconv.Quote(check.Value)
			if !strings.Contains(observation.Output, check.Value) {
				result.Status = CheckPass
				result.Actual = "absent"
			} else {
				result.Status = CheckFail
				result.Actual = "present"
				result.Detail = "final output contained forbidden text"
			}
		case agent.MissionCheckOutputRegex:
			result.Expected = check.Value
			matched, err := regexp.MatchString(check.Value, observation.Output)
			if err != nil {
				result.Detail = err.Error()
			} else if matched {
				result.Status = CheckPass
				result.Actual = "matched"
			} else {
				result.Status = CheckFail
				result.Actual = "not matched"
				result.Detail = "final output did not match the required pattern"
			}
		case agent.MissionCheckRequiredTool:
			result.Expected = check.Tool
			matched := false
			uncertain := false
			for _, tool := range observation.Tools {
				if tool.Name == check.Tool {
					matched = true
					switch tool.Status {
					case "succeeded":
						result.Status = CheckPass
						result.Actual = "succeeded"
					case "failed":
						if result.Status != CheckPass {
							result.Actual = "failed"
						}
					default:
						uncertain = true
						if result.Status != CheckPass {
							result.Actual = tool.Status
							if result.Actual == "" {
								result.Actual = "status unknown"
							}
						}
					}
				}
			}
			if result.Status == CheckPass {
				break
			}
			if uncertain {
				result.Status = CheckUnknown
				result.Detail = "required tool was observed without a confirmed successful result"
			} else {
				result.Status = CheckFail
				if matched {
					result.Detail = "required tool failed"
				} else {
					result.Detail = "required tool was not observed"
				}
			}
		case agent.MissionCheckMaxDuration:
			limit, _ := time.ParseDuration(strings.TrimSpace(check.Duration))
			result.Expected = limit.String()
			if observation.Duration == nil {
				result.Detail = "duration was not measured"
			} else {
				result.Actual = observation.Duration.String()
				if *observation.Duration <= limit {
					result.Status = CheckPass
				} else {
					result.Status = CheckFail
					result.Detail = "run exceeded the maximum duration"
				}
			}
		case agent.MissionCheckMaxCost:
			result.Expected = formatUSD(*check.CostUSD)
			if observation.CostUSD == nil {
				result.Detail = "cost was not measured"
			} else {
				result.Actual = formatUSD(*observation.CostUSD)
				if *observation.CostUSD <= *check.CostUSD {
					result.Status = CheckPass
				} else {
					result.Status = CheckFail
					result.Detail = "run exceeded the maximum cost"
				}
			}
		case agent.MissionCheckClaim:
			result.Expected = check.Claim
			result.Detail = "business outcome requires trusted external verification"
		}
		results = append(results, result)
	}
	results = append(results, evaluateMissionLimits(contract.Limits, observation)...)

	return MissionEvaluation{Verification: verificationFromChecks(results), Checks: results}
}

func evaluateMissionLimits(limits agent.MissionLimits, observation Observation) []CheckResult {
	results := make([]CheckResult, 0, 3)
	if limits.MaxCostUSD != nil {
		result := CheckResult{ID: "limit-max-cost", Type: agent.MissionCheckMaxCost,
			Description: "run stayed within the mission cost limit", Status: CheckUnknown,
			Expected: formatUSD(*limits.MaxCostUSD)}
		if observation.CostUSD == nil {
			result.Detail = "cost was not measured"
		} else {
			result.Actual = formatUSD(*observation.CostUSD)
			if *observation.CostUSD <= *limits.MaxCostUSD {
				result.Status = CheckPass
			} else {
				result.Status = CheckFail
				result.Detail = "run exceeded the mission cost limit"
			}
		}
		results = append(results, result)
	}
	if strings.TrimSpace(limits.MaxDuration) != "" {
		limit, err := time.ParseDuration(strings.TrimSpace(limits.MaxDuration))
		result := CheckResult{ID: "limit-max-duration", Type: agent.MissionCheckMaxDuration,
			Description: "run stayed within the mission duration limit", Status: CheckUnknown,
			Expected: strings.TrimSpace(limits.MaxDuration)}
		if err != nil || limit <= 0 || limit > 24*time.Hour {
			result.Detail = "mission duration limit is invalid"
		} else if observation.Duration == nil {
			result.Detail = "duration was not measured"
		} else {
			result.Actual = observation.Duration.String()
			if *observation.Duration <= limit {
				result.Status = CheckPass
			} else {
				result.Status = CheckFail
				result.Detail = "run exceeded the mission duration limit"
			}
		}
		results = append(results, result)
	}
	if limits.AllowedTools != nil {
		allowed := make(map[string]struct{}, len(*limits.AllowedTools))
		for _, name := range *limits.AllowedTools {
			allowed[strings.TrimSpace(name)] = struct{}{}
		}
		disallowed := make([]string, 0)
		for _, used := range observation.Tools {
			if _, ok := allowed[used.Name]; !ok {
				disallowed = append(disallowed, used.Name)
			}
		}
		result := CheckResult{ID: "limit-allowed-tools", Type: agent.MissionCheckAllowedTools,
			Description: "run used only mission-approved tools", Status: CheckPass,
			Expected: strings.Join(*limits.AllowedTools, ", ")}
		if len(disallowed) > 0 {
			result.Status = CheckFail
			result.Actual = strings.Join(disallowed, ", ")
			result.Detail = "run used a tool outside the mission allowlist"
		}
		results = append(results, result)
	}
	return results
}

func verificationFromChecks(checks []CheckResult) CheckStatus {
	if len(checks) == 0 {
		return CheckUnknown
	}
	unknown := false
	for _, check := range checks {
		switch check.Status {
		case CheckFail:
			return CheckFail
		case CheckUnknown:
			unknown = true
		case CheckPass:
		default:
			unknown = true
		}
	}
	if unknown {
		return CheckUnknown
	}
	return CheckPass
}

func formatUSD(v float64) string {
	return "$" + strconv.FormatFloat(v, 'f', -1, 64)
}
