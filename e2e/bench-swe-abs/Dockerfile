# Benchmark harness: swebench 4.1.0 (make_test_spec + log parsers) and pandas to read the SWE-ABS parquet.
# The network is used only at build time; the benchmark runs it with --network none.
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir swebench==4.1.0 pandas==2.3.2 pyarrow==21.0.0 unidiff==0.7.5
