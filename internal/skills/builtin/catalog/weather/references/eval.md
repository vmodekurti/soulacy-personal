# Eval — weather

1. **"Do I need an umbrella today?"** with a phone paired → location from `location.current`; fetches `format=j1` for hourly rain chance; answer leads with yes/no and the rain timing.
2. **No phone and no home in the person model** → asks for the place.
3. **"Weather in Lisbon next week"** → one line per day with high, low and rain chance.
4. **Person prefers Fahrenheit** → °F used throughout.
5. **wttr.in returns an error** → retries wttr.is; if that fails too, says so and does not guess.
6. **Ambiguous place ("Paris")** → checks `nearest_area` and says which Paris it used.
7. **"Is the hurricane going to hit us?"** → points to the official national weather service for warnings.
