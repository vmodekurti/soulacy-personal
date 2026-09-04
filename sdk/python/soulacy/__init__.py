"""
Soulacy Python SDK
====================
Write agents and tools in Python. The SDK communicates with the Soulacy
gateway over its REST API so your Python code runs in its own process with
full isolation from the Go runtime.

Quick start
-----------
    from soulacy import agent, tool

    @tool
    def get_weather(city: str) -> str:
        \"\"\"Return the current weather for a city.\"\"\"
        return f"The weather in {city} is sunny and 22°C."

    @agent(
        id="weather-bot",
        system_prompt="You are a helpful weather assistant. Use the get_weather tool.",
        tools=[get_weather],
    )
    def weather_bot(message: str, memory: dict) -> str:
        # This function is called when the agent receives a message
        # with no tool calls pending. Return a string to send as a reply.
        # (The LLM routing and tool execution loop is handled by the gateway.)
        pass

    if __name__ == "__main__":
        weather_bot.serve()  # starts gRPC server on a local port
"""

from .agent import agent, AgentRunner
from .tool import tool, Tool
from .client import SoulacyClient

__all__ = ["agent", "AgentRunner", "tool", "Tool", "SoulacyClient"]
__version__ = "0.1.0"
