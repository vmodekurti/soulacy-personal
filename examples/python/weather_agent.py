"""
examples/python/weather_agent.py
=================================
A complete Python-defined agent example using the Soulacy SDK.

This demonstrates:
  - Defining tools with the @tool decorator
  - Defining an agent with the @agent decorator
  - Registering the agent with the running Soulacy gateway
  - Serving a local tool execution server

Run:
    python weather_agent.py

Then test it:
    sy chat --agent weather-bot "What's the weather in Tokyo?"

Or from Python:
    from soulacy import SoulacyClient
    cs = SoulacyClient()
    print(cs.chat("weather-bot", "What's the weather in Paris?"))
"""

from soulacy import agent, tool


@tool
def get_weather(city: str) -> str:
    """Return the current weather conditions for a city.

    In production, replace this with a real weather API call
    (e.g. Open-Meteo, OpenWeatherMap, WeatherAPI).
    """
    # Mock implementation — replace with real API
    weather_data = {
        "tokyo":  "Partly cloudy, 24°C, humidity 68%",
        "london": "Overcast, 14°C, chance of rain 80%",
        "paris":  "Sunny, 21°C, light breeze",
        "sydney": "Clear, 18°C, UV index 6",
    }
    key = city.lower().strip()
    return weather_data.get(key, f"Weather data unavailable for {city}.")


@tool
def get_forecast(city: str, days: int = 3) -> str:
    """Return a multi-day weather forecast for a city.

    Args:
        city: The city name.
        days: Number of forecast days (1-7).
    """
    import json
    if days < 1:
        days = 1
    if days > 7:
        days = 7
    # Mock forecast
    forecast = [
        {"day": f"Day {i+1}", "condition": "Partly cloudy", "high": 22 + i, "low": 14 + i}
        for i in range(days)
    ]
    return json.dumps(forecast, indent=2)


@agent(
    id="weather-bot",
    name="Weather Bot",
    system_prompt="""You are a friendly weather assistant.
Use the get_weather tool to check current conditions and get_forecast for multi-day forecasts.
Always mention temperature in both Celsius.
Be concise and friendly.""",
    tools=[get_weather, get_forecast],
    channels=["http", "telegram"],
    llm_provider="ollama",
    llm_model="llama3",
)
def weather_bot():
    """Weather agent — answers questions about weather using real tools."""
    pass


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Soulacy Weather Bot")
    parser.add_argument("--port", type=int, default=19001, help="Tool server port")
    parser.add_argument("--gateway", default=None, help="Soulacy gateway URL")
    parser.add_argument("--api-key", default=None, help="Gateway API key")
    parser.add_argument("--no-register", action="store_true", help="Skip auto-registration")
    args = parser.parse_args()

    weather_bot.serve(
        port=args.port,
        auto_register=not args.no_register,
        gateway_url=args.gateway,
        api_key=args.api_key,
    )
