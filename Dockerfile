FROM mcr.microsoft.com/dotnet/sdk:10.0-alpine AS build

ARG RUNTIME="linux-musl-x64"

WORKDIR /app

# Separate layers here to avoid redoing dependencies on code change.
COPY *.sln .
COPY *.csproj .
RUN dotnet restore

# Now the code.
COPY . .
RUN dotnet publish -r ${RUNTIME} -c Release -o out

FROM mcr.microsoft.com/dotnet/runtime-deps:10.0-alpine AS runtime
WORKDIR /app
COPY --from=build /app/out .

ENTRYPOINT ["./docker_exporter"]
