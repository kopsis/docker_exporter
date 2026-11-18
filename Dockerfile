ARG RUNTIME="linux-musl-x64"
ARG DOTNET_VER="10.0"

FROM mcr.microsoft.com/dotnet/sdk:${DOTNET_VER}-alpine AS build
WORKDIR /app

# Separate layers here to avoid redoing dependencies on code change.
COPY *.sln .
COPY *.csproj .
RUN dotnet restore

# Now the code.
COPY . .
RUN dotnet publish -r ${RUNTIME} -c Release -o out

FROM mcr.microsoft.com/dotnet/runtime-deps:${DOTNET_VER}-alpine AS runtime
WORKDIR /app
COPY --from=build /app/out .

ENTRYPOINT ["./docker_exporter"]
