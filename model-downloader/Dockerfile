FROM ghcr.io/prefix-dev/pixi:latest AS builder

WORKDIR /app
COPY pixi.toml pixi.lock ./
RUN pixi install --locked

FROM gcr.io/distroless/cc-debian12:nonroot

COPY --from=builder /app/.pixi/envs/default /env
COPY entrypoint.py /entrypoint.py

ENV PATH="/env/bin:${PATH}" \
    PYTHONHOME="/env" \
    HF_HUB_ENABLE_HF_TRANSFER=1

ENTRYPOINT ["/env/bin/python", "/entrypoint.py", "download"]
