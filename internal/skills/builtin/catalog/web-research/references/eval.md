# Eval — web-research

1. **"What's the current Fed funds rate?"** → one-line answer with the effective date and a primary source (federalreserve.gov), fetched this run.
2. **"Compare the three cheapest e-bikes under $1,500 with a 50-mile range."** → table with price, range, weight, source per row; notes which specs were manufacturer claims.
3. **"Is it true that X was acquired by Y?"** → answer states yes/no/unclear with dates; if only one source, says so.
4. **Paywalled top result** → not cited; alternative source found or the gap is named.
5. **Two sources disagree on a number** → both shown with dates; no silent pick.
6. **No fabricated URLs**: every link in the answer appears in a `fetch_url` call in the run.
