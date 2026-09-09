.DEFAULT_GOAL := help

COMPOSE ?= docker compose
GO ?= go
RACE_IMAGE ?= jungle-gaming-e2e-test
E2E_TAGS ?= integration hostrecovery
READY_TIMEOUT ?= 90

.PHONY: help build up down ps logs wait-ready check-compose reset-local vet test test-e2e test-recovery test-race test-all

help: ## Lista os atalhos de avaliação disponíveis.
	@awk 'BEGIN { FS = ":.*##"; printf "Uso: make <alvo>\n\nAlvos:\n" } /^[a-zA-Z0-9_-]+:.*##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Constrói as imagens usadas pelo Docker Compose.
	$(COMPOSE) build

up: ## Inicia o ambiente manual do Compose.
	$(COMPOSE) up --build -d

down: ## Para o Compose sem remover os volumes locais.
	$(COMPOSE) down

ps: ## Mostra o estado dos serviços do Compose.
	$(COMPOSE) ps

logs: ## Acompanha os últimos logs do ambiente manual.
	$(COMPOSE) logs --follow --tail=100

wait-ready: ## Espera as três APIs ficarem prontas (READY_TIMEOUT=90 por padrão).
	@deadline=$$(( $$(date +%s) + $(READY_TIMEOUT) )); \
	for port in 8080 8082 8083; do \
		until curl --silent --show-error --fail http://localhost:$$port/health/ready >/dev/null; do \
			if [ $$(date +%s) -ge $$deadline ]; then \
				echo "API na porta $$port não ficou pronta em $(READY_TIMEOUT)s" >&2; exit 1; \
			fi; \
			sleep 2; \
		done; \
	done
	@echo "APIs 8080, 8082 e 8083 prontas."

check-compose: up wait-ready ## Sobe o pacote que será avaliado e confirma readiness.

reset-local: ## Remove volumes do Compose; exige CONFIRM=1.
	@test "$(CONFIRM)" = "1" || (echo "Use: make reset-local CONFIRM=1" >&2; exit 1)
	$(COMPOSE) down -v

vet: ## Executa a análise estática do Go.
	$(GO) vet ./...

test: ## Executa a suíte rápida sem dependências externas.
	$(GO) test -count=1 ./...

test-e2e: ## Executa E2E isolado com PostgreSQL, Keycloak e LocalStack reais.
	$(GO) test -count=1 -tags="$(E2E_TAGS)" ./integration -v

test-recovery: ## Executa apenas os cenários E2E que reiniciam APIs efêmeras.
	$(GO) test -count=1 -tags="$(E2E_TAGS)" ./integration -run 'TestConsumerRedeliversAfterCommitBeforeDelete|TestRestartPreservesIdempotencyAndPendingReference' -v

test-race: ## Executa a suíte E2E em Linux com o detector de corridas.
	docker build --no-cache -f Dockerfile.test -t $(RACE_IMAGE) .
	docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
		-e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
		$(RACE_IMAGE) test -count=1 -race -tags="$(E2E_TAGS)" ./integration -v

test-all: vet test test-e2e test-race ## Executa todas as validações da entrega.
