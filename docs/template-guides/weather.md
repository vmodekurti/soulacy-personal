# Weather

A weather agent that reports current conditions and forecasts for any location,
on demand in Chat or on a schedule to a channel.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | Any configured provider |
| Tools | `get_weather` | Fetches conditions/forecast |
| Secrets | `WEATHER_API_KEY` (if the tool uses a keyed provider) | Stored in the vault |
| Channels | Optional | For scheduled delivery |
| Schedule | Optional | e.g. daily morning briefing |

## Install

1. Open the template in the **Template Install Wizard** and review the readiness
   checklist — it flags a missing provider or `WEATHER_API_KEY`.
2. Run the **mock test** ("weather in London") to confirm the workflow shape with
   no external call.
3. Add `WEATHER_API_KEY`, then run the **real test**.
4. (Optional) Set a schedule ("every day at 7am") and an output channel.
5. Install.

## Verify

```bash
sy eval --agent weather --suite evals/golden/weather.yaml
```

## Troubleshooting

- No data returned → confirm `WEATHER_API_KEY` is set; see
  [Common failures](../troubleshooting/common-failures.md).
- Scheduled message not delivered → set a default outbound bot or a destination
  on the schedule.
