# Test data

`page1.xml` and `page2.xml` are two pages of a synthetic `DescribeSpotPriceHistory` response in the EC2 query (XML) format.
These files are not recorded from the live API. They follow the documented format.
A recorded response is welcome: open a pull request.

Content: `g6.xlarge` and `g6.2xlarge` in `eu-north-1a` and `eu-north-1b`, from 2026-10-01 for 16 days.
The price changes every 3 hours at minute 7. From 00:00 to 06:00 UTC the price is 20 % lower.
`g6.xlarge` in `eu-north-1a` is always the cheapest: 0.35 USD/h by day and 0.28 USD/h at night, plus 0.001 USD/h times (day mod 3).
The records are newest first, as AWS returns them.
