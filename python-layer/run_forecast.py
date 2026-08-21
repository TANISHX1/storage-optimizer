from pathlib import Path

from core.synthetic_provider import SyntheticDataProvider
from forecast.fast_forecast import run_fast_forecast
from forecast.slow_forecast import run_slow_forecast

provider = SyntheticDataProvider(
    Path("data/synthetic_arima.csv")
)

snapshots = provider.get_snapshots()

print("=== FAST ===")

fast = run_fast_forecast(
    snapshots,
    forecast_days=30,
    validation_size=3,
)

print("Model:", fast.model_name)
print("MAE:", fast.mae_bytes)
print("RMSE:", fast.rmse_bytes)
print("MAPE:", fast.mape_percent)
print("Points:", len(fast.forecast_points))

print("\n=== SLOW ===")

slow = run_slow_forecast(
    snapshots,
    forecast_days=365,
    validation_size=30,
)

print("Model:", slow.model_name)
print("ARIMA:", slow.arima_order)
print("MAE:", slow.mae_bytes)
print("RMSE:", slow.rmse_bytes)
print("MAPE:", slow.mape_percent)
print("Points:", len(slow.forecast_points))
