# Jungle Gaming — execução e validação

Este README é o roteiro de avaliação do projeto: sobe o ambiente local, mostra
como autenticar e percorrer o fluxo financeiro sem seed, e separa os comandos
rápidos da bateria E2E isolada. O enunciado técnico original foi preservado em
[`docs/technical-challenge.md`](docs/technical-challenge.md).

## Subir o projeto localmente

Há dois jeitos intencionalmente separados de conhecer o sistema:

- o **ambiente manual** usa Docker Compose, mantém os dados locais entre reinícios e serve para percorrer a API pelo Swagger ou por chamadas HTTP;
- a **bateria E2E** usa Testcontainers e cria tudo do zero para cada execução. Ela não aproveita o banco, as filas, os volumes nem as APIs do Compose.

O `.env` é propositalmente público e versionado neste desafio. Ele contém apenas
configuração local do Keycloak e das portas, usada para reduzir o tempo de setup
na avaliação; não há segredos de produção nele. As credenciais SQS são criadas no
startup pelo broker e não são versionadas. `.env.example` é a referência dos
mesmos valores. Para usar outras portas ou valores locais, ajuste o `.env` na sua
máquina sem adicionar credenciais reais ao repositório. Em seguida:

```sh
docker compose up --build -d
docker compose ps
curl --fail-with-body http://localhost:8080/health/ready
```

Espere o `migrate` terminar com sucesso e as três APIs aparecerem como em execução. A resposta de readiness deve informar `database`, as duas filas SQS e `identityProvider` como `up`. A partir daí, abra `http://localhost:8080/swagger/index.html` ou siga o fluxo HTTP abaixo.

## Atalhos para avaliação

Quem tiver GNU Make pode usar `make help` para listar os comandos. Os atalhos não
escondem ambiente ou dados: `make check-compose` sobe o pacote normal e espera as
três APIs; `make manual-flow` percorre a API como uma integração externa; `make down` o encerra sem remover volumes. `make test` cobre o Go sem
dependências externas, `make test-fuzz` explora os contratos de `Money`,
`make test-e2e` cria a infraestrutura isolada com Testcontainers e
`make test-race` repete a E2E em Linux com `-race`. `make test-regression`
executa somente os casos de token expirado e reversões inválidas.

```sh
make check-compose
make manual-flow
make test
make test-fuzz
make test-regression
make test-e2e
make test-race
```

`make test-all` encadeia `go vet`, testes rápidos, fuzzing, E2E e race detector.
O fuzzing usa dez segundos por alvo por padrão; ajuste com, por exemplo,
`make test-fuzz FUZZ_TIME=1m`. Para apagar deliberadamente os volumes do Compose,
use `make reset-local CONFIRM=1`; nenhum outro alvo remove dados locais.

### Evidência de fuzzing

Na validação de 9 de setembro de 2026, os dois alvos foram executados por dez
segundos cada e completaram **3.148.324 execuções sem falhas** após a correção
da desserialização do limite `math.MinInt64`:

| Alvo | Execuções | Resultado |
| --- | ---: | --- |
| `FuzzParseMoneyRoundTrip` | 1.734.204 | `PASS` |
| `FuzzMoneyJSONRoundTrip` | 1.414.120 | `PASS` |
| **Total** | **3.148.324** | **Nenhuma falha encontrada** |

Essa contagem é um retrato da execução registrada, não uma quantidade fixa:
ela varia conforme hardware, carga da máquina, número de workers e `FUZZ_TIME`.
O resultado reproduzível é a aprovação das propriedades verificadas pelos
fuzzers durante o tempo configurado.

## O que o Compose inicia

O comando de subida executa o serviço `migrate` com `golang-migrate` antes de
iniciar as APIs. Em um banco criado pela versão anterior, ele apenas acrescenta
a coluna interna `dirty` na tabela de controle, preservando a versão já aplicada.

## Banco manual sem seed

As migrations versionadas criam somente schema: elas não inserem carteiras,
transações, ledger, inbox ou outbox. Portanto, a primeira execução de
`docker compose up --build -d` começa sem registros de domínio e o fluxo pode
ser executado integralmente pelo Swagger ou pela API.

Para descartar dados locais de uma execução manual anterior e voltar a esse
estado vazio, pare o ambiente e remova **somente** seus volumes locais:

```sh
docker compose down -v
docker compose up --build -d
```

Isso apaga os dados locais do Compose; não é necessário nem é usado pelos testes
E2E.

## Acesso local pronto para uso

As credenciais OAuth abaixo são públicas apenas para a avaliação local. O acesso
SQS, em contraste, usa identidades IAM efêmeras de menor privilégio, criadas pelo
bootstrap do broker e montadas somente nos containers autorizados.

| Serviço | Endereço | Credencial |
| --- | --- | --- |
| Swagger | http://localhost:8080/swagger/index.html | Use **Authorize** e escolha o fluxo desejado abaixo. |
| Keycloak Admin | http://localhost:8081 | `admin` / `admin-local` |
| Swagger interno | `client_id`: `swagger-internal` | `wallet-tester` / `wallet-tester-local` |
| Swagger provider A | `client_id`: `swagger-provider-a` | `provider-a-tester` / `provider-a-tester-local` |
| Swagger provider B | `client_id`: `swagger-provider-b` | `provider-b-tester` / `provider-b-tester-local` |
| Swagger provider C | `client_id`: `swagger-provider-c` | `provider-c-tester` / `provider-c-tester-local` |
| Provider A service-to-service | client `provider-a` | secret `provider-a-local-secret` |
| Provider B service-to-service | client `provider-b` | secret `provider-b-local-secret` |
| Provider C service-to-service | client `provider-c` | secret `provider-c-local-secret` |
| Carteiras service-to-service | client `wallet-service` | secret `wallet-service-local-secret` |
| MiniStack SQS | http://localhost:4566 | IAM com `AUTH=true`; as credenciais não são expostas no host. |

No modal **Available authorizations** do Swagger, preencha os campos desta forma:

| Autorização | Operações | `client_id` | `client_secret` | Login no Keycloak |
| --- | --- | --- | --- | --- |
| `InternalOAuth` | Abrir e consultar carteiras | `swagger-internal` | Deixe vazio | `wallet-tester` / `wallet-tester-local` |
| `ProviderOAuth` (provider A) | Enviar e consultar apostas do provider A | `swagger-provider-a` | Deixe vazio | `provider-a-tester` / `provider-a-tester-local` |
| `ProviderOAuth` (provider B) | Enviar e consultar apostas do provider B | `swagger-provider-b` | Deixe vazio | `provider-b-tester` / `provider-b-tester-local` |
| `ProviderOAuth` (provider C) | Enviar e consultar apostas do provider C | `swagger-provider-c` | Deixe vazio | `provider-c-tester` / `provider-c-tester-local` |

Esses clients do Swagger são públicos e usam Authorization Code com PKCE;
por isso, não informe `client_secret`. Depois de preencher o `client_id`, clique
em **Authorize** e use o login indicado na tabela.

### SQS com IAM local

O requisito de controle da mensageria é aplicado pelo MiniStack, uma das opções
aceitas pelo enunciado. O bootstrap cria as quatro filas FIFO, as DLQs, o redrive e
as identidades abaixo antes de qualquer API iniciar:

| Identidade | Permissões concedidas |
| --- | --- |
| `jungle-gaming-application` | Resolve filas, consulta atributos, recebe/remove/estende visibilidade e publica mensagens; não pode administrar a infraestrutura. |
| `jungle-gaming-provider-publisher` | Apenas resolve e publica mensagens. |
| `jungle-gaming-event-consumer` | Apenas resolve, consulta atributos, recebe/remove e estende visibilidade. |
| `jungle-gaming-test-harness` | Acesso às quatro filas, exclusivamente na suíte E2E. |
| `jungle-gaming-denied` | Nenhuma permissão SQS; existe para provar a negação no E2E. |

As chaves são geradas pelo broker e ficam em volumes Docker separados. O processo
da API recebe somente o profile `application`; não pode criar/deletar filas,
alterar atributos, listar filas ou administrar IAM. `SQS_CREATE_QUEUES` fica
`false` para que a aplicação restrita não tente alterar a infraestrutura.

O MiniStack 1.5 aplica o conjunto de ações por identidade, mas ainda não compara
ARN de fila no avaliador IAM. Por isso as policies usam `Resource: "*"` e reduzem
o privilégio por ação; a separação de propósito das filas continua protegida pela
validação de domínio do consumidor, como pede o enunciado. Em AWS real ou
LocalStack licenciado, a mesma matriz deve ser refinada para os ARNs individuais
das quatro filas.

O Compose local também define `SQS_DISABLE_MESSAGE_CHECKSUM_VALIDATION=true`.
É uma compatibilidade restrita ao MiniStack: no reenvio FIFO deduplicado ele
retorna o MD5 do primeiro corpo, enquanto o banco pode normalizar a ordem das
chaves JSON. A validação permanece `false` por padrão e deve continuar assim em
AWS SQS.

Para testar manualmente uma identidade de integração sem revelar chaves, use o
container de ferramentas. Este comando publica na fila de entrada usando o profile
de publisher; trocar o profile por `denied` retorna `AccessDeniedException`.

```sh
docker compose --profile tools run --rm \
  -e AWS_PROFILE=provider-publisher \
  sqs-cli sqs get-queue-url --queue-name wager-transactions.fifo
```

## Exemplos por rota no Swagger

As rotas `GET` e a reconciliação não recebem payload. Use os IDs devolvidos
pelas rotas de criação e processamento nos respectivos parâmetros de path.

| Rota | Autorização | O que informar |
| --- | --- | --- |
| `GET /health/live` | Pública | Nenhum parâmetro ou payload |
| `GET /health/ready` | Pública | Nenhum parâmetro ou payload |
| `GET /metrics` | Pública | Nenhum parâmetro ou payload |
| `POST /wallets` | `InternalOAuth` | Payload de abertura mostrado abaixo |
| `GET /wallets/{walletId}` | `InternalOAuth` | `walletId` devolvido ao abrir a carteira |
| `GET /wallets/{walletId}/ledger` | `InternalOAuth` | `walletId`, `limit` de 1 a 100 e `cursor` apenas para a próxima página |
| `POST /wallets/{walletId}/reconciliation` | `InternalOAuth` | Somente `walletId`; não recebe payload |
| `POST /wagering/transactions` | `ProviderOAuth` | `Idempotency-Key` e um dos payloads de transação abaixo |
| `GET /wagering/transactions/{transactionId}` | `InternalOAuth` | ID interno `transactionId` devolvido no processamento |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | `ProviderOAuth` | Por exemplo, `provider-a` e `bet-001` |

### Abrir uma carteira

Em `POST /wallets`, use um `playerId` UUID novo. O tipo `OPENING` é criado
automaticamente por essa rota e não pode ser enviado como uma transação externa.

```json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": {
    "amount": "100.00",
    "currency": "BRL"
  }
}
```

Copie o campo `id` da resposta e use-o como `walletId` nos exemplos seguintes.
Mantenha também o mesmo `playerId` e a moeda da carteira.

### Transações de aposta

Em `POST /wagering/transactions`, o `providerId` precisa corresponder ao client
usado no `ProviderOAuth`. Cada nova operação exige um
`externalTransactionId` e uma `Idempotency-Key` novos. O `X-Correlation-ID` é
opcional; se omitido, a API gera um identificador.

Substitua `<WALLET_ID>` pelo `id` recebido na abertura da carteira. Os exemplos
abaixo formam uma sequência válida usando a mesma carteira e rodada.

#### BET — debita o valor da aposta

`Idempotency-Key: provider-a:bet-001`

```json
{
  "externalTransactionId": "bet-001",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" },
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "providerId": "provider-a",
  "roundId": "round-001",
  "walletId": "<WALLET_ID>"
}
```

#### WIN — credita o prêmio

`Idempotency-Key: provider-a:win-001`

```json
{
  "externalTransactionId": "win-001",
  "gameId": "fortune-chimp",
  "kind": "WIN",
  "money": { "amount": "40.00", "currency": "BRL" },
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "providerId": "provider-a",
  "referenceExternalTransactionId": "bet-001",
  "roundId": "round-001",
  "walletId": "<WALLET_ID>"
}
```

A referência a uma `BET` da mesma rodada é opcional para `WIN`; remova
`referenceExternalTransactionId` quando o prêmio não possuir essa referência.

#### LOSS — registra a perda sem nova movimentação

`Idempotency-Key: provider-a:loss-001`

```json
{
  "externalTransactionId": "loss-001",
  "gameId": "fortune-chimp",
  "kind": "LOSS",
  "money": { "amount": "0.00", "currency": "BRL" },
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "providerId": "provider-a",
  "roundId": "round-001",
  "walletId": "<WALLET_ID>"
}
```

`LOSS` exige valor `0.00`, não cria lançamento no ledger e não altera o saldo.

#### REFUND — devolve integralmente uma BET

`Idempotency-Key: provider-a:refund-001`

```json
{
  "externalTransactionId": "refund-001",
  "gameId": "fortune-chimp",
  "kind": "REFUND",
  "money": { "amount": "25.00", "currency": "BRL" },
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "providerId": "provider-a",
  "referenceExternalTransactionId": "bet-001",
  "roundId": "round-001",
  "walletId": "<WALLET_ID>"
}
```

#### ROLLBACK — desfaz integralmente outra movimentação

Este exemplo desfaz a `WIN` anterior. Portanto, o valor precisa ser exatamente
o mesmo da operação referenciada.

`Idempotency-Key: provider-a:rollback-001`

```json
{
  "externalTransactionId": "rollback-001",
  "gameId": "fortune-chimp",
  "kind": "ROLLBACK",
  "money": { "amount": "40.00", "currency": "BRL" },
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "providerId": "provider-a",
  "referenceExternalTransactionId": "win-001",
  "roundId": "round-001",
  "walletId": "<WALLET_ID>"
}
```

`REFUND` e `ROLLBACK` exigem `referenceExternalTransactionId`, identidade
compatível e o valor integral da transação referenciada. Se a referência ainda
não existir, a resposta pode ser `202 PENDING_REFERENCE` até ela ser resolvida.
Reenviar exatamente o mesmo payload com a mesma `Idempotency-Key` é seguro e
deve retornar `idempotentReplay: true`, sem aplicar novamente a movimentação.

As filas são criadas automaticamente: `wager-transactions.fifo`, sua DLQ,
`wager-events.fifo` e sua DLQ. Para testar manualmente no Swagger, autentique
com `swagger-internal` para abrir/consultar carteiras e com
`swagger-provider-a`, `swagger-provider-b` ou `swagger-provider-c` para enviar
ou consultar apostas.

## Fluxo manual completo, sem dados pré-criados

O script abaixo executa o roteiro como uma integração externa: usa
`client_credentials` no Keycloak, cria uma carteira e envia BET/WIN com IDs
aleatórios. Ele não depende de registros pré-existentes e funciona mesmo se o
banco local já tiver dados de execuções anteriores.

Use Bash (Linux, macOS, WSL ou Git Bash) e `curl`. Não é necessário instalar
`jq`, Python, Go, um cliente PostgreSQL ou um cliente SQS. O script usa `uuidgen`
quando disponível e possui fallbacks para Linux e OpenSSL.

```sh
make manual-flow
# ou, depois de subir o Compose:
bash scripts/manual-flow.sh
```

O roteiro autentica os serviços interno, provider A e provider B; verifica health
e métricas; cria e consulta a carteira em réplicas distintas; processa BET e WIN;
prova o replay idempotente; consulta a transação pelos dois contratos; confere o
ledger; executa reconciliação; e confirma que provider B recebe `403` ao tentar
ler uma transação de provider A. No sucesso, imprime IDs gerados e saldo final de
`115.00 BRL`.

Os payloads individuais continuam nas seções anteriores para testes pontuais pelo
Swagger. O script não mostra tokens ou segredos na saída.

Para acompanhar as mensagens de saída, a fila é `wager-events.fifo`; os formatos
e as regras de deduplicação do consumidor estão em `docs/outbound-event-contract.md`.
O Swagger é preferível para uma exploração manual dos demais tipos (`WIN`, `LOSS`,
`REFUND` e `ROLLBACK`), pois permite alterar apenas o corpo e manter o mesmo
fluxo autenticado.

## Health checks

`GET /health/live` confirma somente que o processo está em execução. Use
`GET /health/ready` para diagnosticar dependências: ele informa o estado e a
latência de `database`, `sqsInputQueue`, `sqsEventQueue` e
`identityProvider`. O resultado é `200` quando todos estão disponíveis e
`503` com os checks parciais quando qualquer dependência estiver indisponível.

```json
{
  "status": "ready",
  "checkedAt": "2026-09-09T07:00:00Z",
  "checks": {
    "database": { "status": "up", "latencyMs": 2 },
    "sqsInputQueue": { "status": "up", "latencyMs": 5 },
    "sqsEventQueue": { "status": "up", "latencyMs": 5 },
    "identityProvider": { "status": "up", "latencyMs": 3 }
  }
}
```

## Migrations

Aplicar migrations manualmente (o `up` é também o padrão do Compose):

```sh
docker compose run --rm migrate
```

Reverter uma única versão — pare as APIs antes para não executar schema e
aplicação em versões diferentes:

```sh
docker compose stop api-1 api-2 api-3
docker compose run --rm migrate down 1
```

Para retornar ao estado mais recente, execute novamente `docker compose up -d`.

A migração `000004_wallet_ledger_consistency` adiciona validações deferidas no
commit: o saldo persistido deve corresponder à soma do ledger e toda operação
financeira `PROCESSED` precisa ter lançamento. Assim, uma escrita SQL direta de
saldo sem o ledger é rejeitada pelo próprio PostgreSQL.

## E2E isolado com Testcontainers

Os testes com a tag `integration` não usam os serviços do Compose. O `TestMain`
cria uma rede privada e efêmera com Testcontainers, PostgreSQL novo, MiniStack com
IAM aplicado,
Keycloak e três processos da API em containers independentes. Aplica as migrations
em um banco de teste vazio e confere que não há dados de domínio antes do primeiro
cenário. O único fixture é o realm do Keycloak, limitado aos clients OAuth usados
nos testes. Ao fim da suíte, todos os containers, rede e imagem de teste são
removidos automaticamente.

```sh
go test -count=1 -tags=integration ./integration -v
```

No Windows, execute o detector de corrida em Linux. O container abaixo é apenas
o executor de testes: as dependências e as APIs continuam sendo criadas e
encerradas pelo Testcontainers, sem iniciar o Compose.

```sh
docker build --no-cache -f Dockerfile.test -t jungle-gaming-e2e-test .
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
  jungle-gaming-e2e-test test -count=1 -race -tags="integration hostrecovery" ./integration -v
```

Além de concorrência e recuperação, a suíte cria uma transação real com o
client OAuth `provider-b` e confirma que o token de `provider-a` não consegue
consultá-la nem reexecutá-la. A rejeição não cria nova transação nem altera
carteira ou ledger; o replay pelo provider B continua idempotente. O lifecycle
Fx é iniciado com os workers reais antes de realizar o shutdown. Também confirma
que o client OAuth `provider-c` é aceito pela API com seu claim de provider.
Ela também simula um publisher interrompido depois de `SendMessage` e antes de
confirmar a outbox: após a expiração do lease, outro publisher recupera o mesmo
registro, preserva o `eventId` e confirma a publicação.

Os endereços usados pelo teste são resolvidos dinamicamente; nenhum endpoint,
volume ou fila do ambiente manual é reutilizado.

A build E2E reaproveita camadas válidas do Docker, mas sempre recompila as
camadas invalidadas pelo código atual. Para uma verificação deliberadamente sem
cache, execute `E2E_NO_CACHE=1 make test-e2e` em Linux/macOS ou defina essa
variável no PowerShell antes do comando equivalente.

## Cenários de recuperação que controlam containers

Os dois cenários abaixo interrompem e recriam containers de API do próprio
Testcontainers. Eles não tocam no Compose:

```sh
go test -tags="integration hostrecovery" ./integration -run "TestConsumerRedeliversAfterCommitBeforeDelete|TestRestartPreservesIdempotencyAndPendingReference" -v -count=1
```

Eles cobrem, respectivamente, a queda abrupta entre o commit e a remoção da
mensagem SQS e a preservação de idempotência/referências pendentes após reiniciar
as APIs.

## Outros comandos

```sh
go test -count=1 ./...
go vet ./...
docker compose down
```

`go test -count=1 ./...` é a verificação rápida do código sem dependências
externas. A execução com Testcontainers é a validação de sistema: ela chama as
APIs reais, obtém tokens reais do Keycloak, usa SQS real do MiniStack com IAM e aplica
migrations em um PostgreSQL vazio. Rode a versão Linux com `-race` antes de uma
entrega; ela também inclui os cenários que reiniciam containers durante o fluxo.

Para parar o ambiente manual sem perder a sessão, use `docker compose down`.
Para voltar deliberadamente ao banco vazio, use `docker compose down -v` e suba
novamente. Esse segundo comando remove os volumes locais do Compose, mas nunca é
necessário para os testes E2E.
