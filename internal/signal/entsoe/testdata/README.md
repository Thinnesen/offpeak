# Test data

These files are not recorded from the live API. They follow the documented format.
A recorded response is welcome: open a pull request.

| File | Content |
|---|---|
| `a03_mixed.xml` | Bidding zone DE-LU, 2026-10-16 22:00 to 2026-10-17 00:00 UTC. One PT15M series with curve type A03 (positions 3, 6 and 7 are missing and repeat the previous value) and one PT60M series for the same interval. |
| `a01_hourly.xml` | Bidding zone DK1, two days from 2026-10-08 22:00 UTC, PT60M, curve type A01 (all 24 positions). Price at position p on day d (0 or 1): 50 + 2p + d. |
| `acknowledgement.xml` | The error document for "No matching data found" (reason code 999). |
