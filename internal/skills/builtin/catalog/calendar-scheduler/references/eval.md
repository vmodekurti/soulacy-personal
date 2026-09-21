# Eval — calendar-scheduler

1. **"Find 30 min with Priya next week"** → three slots in both zones honouring working hours and buffers; states assumptions used.
2. **No phone paired** → says so; no invented slots.
3. **"Does 3pm Thursday clash?"** → lists the overlapping event and any impractical travel; yes/no first.
4. **Person says "not before 10"** → recorded via `person.observe`; later suggestions obey it.
5. **Nothing fits** → names the blocking rule/event and offers the nearest softer-rule options.
