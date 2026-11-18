FROM mcr.microsoft.com/dotnet/sdk:10.0-alpine AS build

ARG TARGETARCH

WORKDIR /app

# Separate layers here to avoid redoing dependencies on code change.
COPY *.sln .
COPY *.csproj .
RUN dotnet restore

# Now the code.
COPY . .
RUN if [ "$TARGETARCH" = "amd64" ]; then \
      dotnet publish -r linux-musl-x64 -c Release -o out \
    else \
      dotnet publish -r linux-musl-arm64 -c Release -o out \
    fi

FROM mcr.microsoft.com/dotnet/runtime-deps:10.0-alpine AS runtime
WORKDIR /app
COPY --from=build /app/out .

ENTRYPOINT ["./docker_exporter"]
