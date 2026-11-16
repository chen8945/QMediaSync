const asRecord = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined

const isStringArray = (value: unknown) =>
  Array.isArray(value) && value.every((item) => typeof item === 'string')

const isSafeInstance = (value: unknown): value is string =>
  typeof value === 'string' && /^\/request-errors\/[a-z\d]{24}$/i.test(value)

// 只验证传输包络，领域 data 由所属 API 解释。
export function isAPIResponse(response: { status: number; data: unknown }): boolean {
  const body = asRecord(response.data)
  if (
    !body ||
    !Number.isInteger(response.status) ||
    body.status !== response.status ||
    !Object.hasOwn(body, 'data') ||
    (body.message !== undefined && typeof body.message !== 'string') ||
    (body.warnings !== undefined && !isStringArray(body.warnings))
  ) {
    return false
  }

  if (response.status >= 200 && response.status < 300) {
    return (
      body.error_code === undefined &&
      body.instance === undefined &&
      body.field_errors === undefined
    )
  }

  return (
    response.status >= 400 &&
    response.status < 600 &&
    typeof body.message === 'string' &&
    body.message.trim() !== '' &&
    typeof body.error_code === 'string' &&
    body.error_code.trim() !== '' &&
    isSafeInstance(body.instance) &&
    body.warnings === undefined &&
    (body.field_errors === undefined ||
      (Array.isArray(body.field_errors) &&
        body.field_errors.every((value) => {
          const field = asRecord(value)
          return (
            field !== undefined &&
            typeof field.field === 'string' &&
            typeof field.message === 'string' &&
            (field.client_id === undefined || typeof field.client_id === 'string')
          )
        })))
  )
}
