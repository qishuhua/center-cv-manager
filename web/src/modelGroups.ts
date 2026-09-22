import type { ModelVersion } from './types'

export interface ModelGroup {
  model: string
  versions: ModelVersion[]
}

export function groupModels(models: ModelVersion[]): ModelGroup[] {
  const groups = new Map<string, ModelVersion[]>()
  for (const item of models) {
    const versions = groups.get(item.model) || []
    versions.push(item)
    groups.set(item.model, versions)
  }
  return [...groups.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([model, versions]) => ({
      model,
      versions: [...versions].sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at)),
    }))
}
