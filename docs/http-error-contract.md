# Contrato de erros HTTP

Este documento define o contrato estável de falhas da API HTTP. O Swagger
descreve os endpoints e schemas de sucesso; este arquivo detalha como um cliente
deve interpretar e tratar respostas não bem-sucedidas.

## Envelope e rastreabilidade

Com exceção de `GET /health/ready`, que possui um corpo de readiness próprio, a
API responde erros com `Content-Type: application/json` e o seguinte envelope:

```json
{
  "error": {
    "code": "INVALID_TOKEN",
    "message": "invalid or expired token"
  }
}
```

`error.code` é a parte estável e própria para decisões de cliente. Ele é sempre
uma string em `UPPER_SNAKE_CASE`. `error.message` é apenas um diagnóstico
humano; clientes não devem fazer parsing dele nem depender de sua redação.

Toda resposta, inclusive as de erro, contém `X-Request-ID`. O cliente pode
enviá-lo para correlacionar uma chamada; se não o fizer, a API cria um UUID. Para
operações, `X-Correlation-ID` liga a chamada aos logs e aos eventos de saída; na
sua ausência, usa-se o `X-Request-ID`.

## Códigos transversais

| HTTP | `error.code` | Quando ocorre | Conduta do cliente |
| --- | --- | --- | --- |
| 400 | `INVALID_JSON` | Corpo ausente, maior que 1 MiB, JSON malformado, campos desconhecidos ou mais de um valor JSON. | Corrigir o corpo; não repetir igual. |
| 400 | `INVALID_REQUEST` | Falha de validação estrutural ou de valor monetário/domínio. | Corrigir os campos; não repetir igual. |
| 401 | `AUTHENTICATION_REQUIRED` | Header `Authorization` ausente ou fora do formato `Bearer <token>`. | Obter e enviar um bearer token. |
| 401 | `INVALID_TOKEN` | Assinatura, emissor, audiência ou expiração do token não é válida. | Renovar/obter token; não alterar a operação. |
| 401 | `INVALID_TOKEN_CLAIMS` | Claims necessárias não podem ser interpretadas. | Corrigir a configuração do cliente/IdP. |
| 403 | `INTERNAL_ROLE_REQUIRED` | Endpoint interno acessado sem a role `wallet-service`. | Usar o client interno autorizado. |
| 403 | `PROVIDER_FORBIDDEN` | `providerId` do corpo/rota não corresponde ao claim do token. | Nunca repetir trocando apenas a chave idempotente; usar a identidade do provider correto. |
| 404 | `NOT_FOUND` | Recurso de domínio inexistente. | Tratar como ausência de recurso. |
| 404 | `ROUTE_NOT_FOUND` | Caminho HTTP não é uma rota da API. | Corrigir método/caminho. |
| 405 | `ROUTE_METHOD_NOT_ALLOWED` | Caminho existe, mas não aceita o método HTTP usado. | Corrigir o método HTTP. |
| 409 | `CONFLICT` | Conflito genérico persistido, como segunda carteira para o mesmo jogador e moeda. | Consultar o recurso existente; não usar retry cego. |
| 409 | `IDEMPOTENCY_PAYLOAD_CONFLICT` | A mesma `Idempotency-Key` foi enviada com conteúdo de negócio diferente. | Gerar nova chave somente para uma nova operação legítima. |
| 409 | `EXTERNAL_TRANSACTION_CONFLICT` | O mesmo par `(providerId, externalTransactionId)` foi enviado com outra chave. | Consultar a transação original; não reaplicar. |
| 503 | `IDENTITY_UNAVAILABLE` | O verificador OIDC ainda não está pronto. | Retry com backoff, preservando a operação e a chave. |
| 503 | `TEMPORARY_UNAVAILABLE` | Dependência técnica indisponível ou timeout de infraestrutura. | Retry com backoff e a mesma chave idempotente. |
| 500 | `INTERNAL_ERROR` | Panic recuperado na borda HTTP. | Retry somente após investigação; informe `X-Request-ID`. |

## Códigos por endpoint

| Endpoint | Códigos adicionais |
| --- | --- |
| `POST /wallets` | `CONFLICT` se já existir carteira para `(playerId, currency)`. |
| `GET /wallets/{walletId}`, `POST /wallets/{walletId}/reconciliation`, `GET /wagering/transactions/{transactionId}` | `INVALID_ID` para UUID inválido; `NOT_FOUND` para recurso ausente. |
| `GET /wallets/{walletId}/ledger` | Além de `INVALID_ID`/`NOT_FOUND`: `INVALID_CURSOR` para cursor opaco inválido e `INVALID_LIMIT` quando `limit` não é inteiro de 1 a 100. |
| `POST /wagering/transactions` | `IDEMPOTENCY_KEY_REQUIRED` quando o header está ausente; `PROVIDER_FORBIDDEN`; conflitos de idempotência; e os resultados de negócio abaixo. |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | `INVALID_ID`, `PROVIDER_FORBIDDEN` e `NOT_FOUND`. |

`INVALID_ID`, `INVALID_CURSOR`, `INVALID_LIMIT` e
`IDEMPOTENCY_KEY_REQUIRED` usam HTTP 400. Eles seguem o mesmo envelope.

## Resultado de negócio da operação de aposta

`POST /wagering/transactions` distingue um erro de protocolo de uma decisão
financeira persistida. Se a operação chegou ao domínio, a resposta é sempre um
`ProcessWagerResult`, inclusive quando o status HTTP não está na faixa 2xx:

```json
{
  "transactionId": "0192f31f-72c2-7a9d-a3a1-5a8e0570aa11",
  "status": "REJECTED",
  "balance": { "amount": "100.00", "currency": "BRL" },
  "failureCode": "INSUFFICIENT_FUNDS",
  "idempotentReplay": false
}
```

| HTTP | `status` no corpo | Significado |
| --- | --- | --- |
| 200 | `PROCESSED` | Operação terminal processada. Também cobre `LOSS`. |
| 202 | `PENDING` ou `PENDING_REFERENCE` | Aceite durável. A referência pendente será resolvida pelo worker; consultar a transação para acompanhar. |
| 422 | `REJECTED` | Regra de negócio rejeitou a operação de forma terminal e auditável. Não há lançamento de débito/crédito para essa operação. |
| 500 | `FAILED` | Falha técnica terminal persistida após esgotar a retomada de referência. Não há lançamento financeiro. |

Os códigos de falha de `REJECTED` e `FAILED` pertencem ao resultado persistido,
não a `error.code`: `WALLET_OWNERSHIP_OR_CURRENCY_MISMATCH`,
`REFERENCE_NOT_PROCESSED`, `REFERENCE_NOT_FOUND`, `REFERENCE_MISMATCH`,
`REFERENCE_AMOUNT_MISMATCH`, `INVALID_REFERENCE_KIND`,
`REFERENCE_ALREADY_REVERSED`, `INSUFFICIENT_FUNDS`,
`INSUFFICIENT_FUNDS_REVERSAL` e `PROCESSING_ATTEMPTS_EXHAUSTED`.

Um replay com a mesma chave e o mesmo conteúdo devolve o resultado histórico e
`idempotentReplay: true`; ele pode, portanto, repetir o HTTP 422 ou 500 da
decisão terminal original sem criar nova movimentação.

## Health checks

`GET /health/live` retorna somente 200. `GET /health/ready` retorna 200 quando
PostgreSQL, as duas filas SQS e o IdP estão disponíveis; caso contrário, retorna
503 com `HealthResponse`, e não com `ProblemResponse`. Cada check informa
`status`, `latencyMs` e uma mensagem genérica sem expor detalhes internos.

