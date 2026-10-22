# Test image for the property gates (e2e/testdata/gates-prop): the same toolchain as e2e/gates/pytest.Dockerfile, plus
# hypothesis for the committed property tests. The pins are frozen with the fixture (cases.json, "image"). It runs
# without network. Build: docker build -t a2dlc-tests-hypothesis:0.1 -f e2e/testdata/gates-prop/tests.Dockerfile e2e/testdata/gates-prop
FROM python:3.12-slim
RUN pip install --no-cache-dir pytest==9.0.3 coverage==7.6.1 hypothesis==6.169.4
