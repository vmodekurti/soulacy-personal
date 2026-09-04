#!/usr/bin/env python3
"""
CSV Analysis Script — used by the csv-analysis Agent Skill.
Prints a JSON summary of a CSV file's structure and statistics.
"""
import json
import sys

def analyze(path: str) -> dict:
    try:
        import pandas as pd
        import numpy as np
    except ImportError:
        return {"error": "pandas and numpy are required: pip install pandas numpy"}

    try:
        # Sample large files to keep analysis fast
        sample_rows = 100_000
        df = pd.read_csv(path, nrows=sample_rows)
        sampled = len(df) >= sample_rows
    except Exception as e:
        return {"error": str(e)}

    result = {
        "rows": len(df),
        "columns": list(df.columns),
        "sampled": sampled,
        "dtypes": {col: str(dtype) for col, dtype in df.dtypes.items()},
        "nulls": {col: int(df[col].isna().sum()) for col in df.columns},
        "numeric_stats": {},
        "top_values": {},
    }

    for col in df.columns:
        if pd.api.types.is_numeric_dtype(df[col]):
            s = df[col].dropna()
            if len(s) > 0:
                result["numeric_stats"][col] = {
                    "min": float(s.min()),
                    "max": float(s.max()),
                    "mean": float(s.mean()),
                    "median": float(s.median()),
                    "std": float(s.std()),
                }
        else:
            top = df[col].value_counts().head(5)
            result["top_values"][col] = {str(k): int(v) for k, v in top.items()}

    return result


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(json.dumps({"error": "Usage: analyze.py <csv_path>"}))
        sys.exit(1)
    print(json.dumps(analyze(sys.argv[1]), indent=2))
