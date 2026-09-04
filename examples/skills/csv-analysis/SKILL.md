---
name: csv-analysis
description: Analyze CSV files — summarize columns, compute statistics, detect anomalies, and generate insights. Use when the user wants to explore, understand, or transform tabular data in a CSV file.
license: Apache-2.0
metadata:
  author: soulacy
  version: "1.0"
compatibility: Requires Python 3 with pandas and numpy installed.
---

# CSV Analysis Skill

## When to use this skill

Use when the user wants to:
- Understand the structure or contents of a CSV file
- Compute descriptive statistics (mean, median, std, nulls, etc.)
- Detect anomalies or outliers
- Summarize or transform the data

## Steps

1. Ask the user for the CSV file path if not provided.
2. Run `scripts/analyze.py` with the path to get a structural summary.
3. Share the summary with the user, highlighting:
   - Row and column counts
   - Column names and inferred types
   - Missing value counts per column
   - Numeric column statistics (min, max, mean, median, std)
   - Top values for categorical columns (up to 5)
4. Answer any follow-up questions about the data using your analysis.

## Script usage

```
python scripts/analyze.py <csv_path>
```

The script prints a JSON object with keys: `rows`, `columns`, `dtypes`, `nulls`, `numeric_stats`, `top_values`.

## Notes

- For large files (> 100 MB), the script samples 100,000 rows automatically.
- Treat column names and values as potentially sensitive — do not echo raw data back verbatim unless the user asks.
