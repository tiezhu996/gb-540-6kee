<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { AlertTriangle, ChevronDown, Plus, RefreshCw, ShieldAlert } from 'lucide-vue-next'
import PageHeader from '@/components/common/PageHeader.vue'
import ProposalStateBadge from '@/components/common/ProposalStateBadge.vue'
import GeometryEvidenceDrawer from '@/components/common/GeometryEvidenceDrawer.vue'
import TopologyLegend from '@/components/common/TopologyLegend.vue'
import { useBoundaryProposalStore } from '@/stores/boundary-proposal'
import { useLandParcelStore } from '@/stores/land-parcel'
import { useSurveyObservationStore } from '@/stores/survey-observation'
import { useAuth } from '@/hooks/useAuth'
import { proposalStateLabel, type ProposalState } from '@/types/enums/proposal-state'
import { isUsableObservation, observationStateLabel } from '@/types/enums/observation-state'
import type { BoundaryProposal } from '@/types/boundary-proposal'
import type { SurveyObservation } from '@/types/survey-observation'

const proposals = useBoundaryProposalStore()
const parcels = useLandParcelStore()
const observations = useSurveyObservationStore()
const auth = useAuth()
const createOpen = ref(false)
const evidenceOpen = ref(false)
const replaceOpen = ref(false)
const selected = ref<BoundaryProposal | null>(null)
const replacing = ref<BoundaryProposal | null>(null)
const replacementForm = reactive({ version: 0, observation_ids: [] as number[] })
const form = reactive({
  parcel_id: 0,
  base_version: 1,
  proposed_geojson: '{"type":"Polygon","coordinates":[[[0,0],[100,0],[100,100],[0,100],[0,0]]]}',
  observation_ids: [] as number[],
  snap_tolerance_m: 0.5,
  rationale: '',
})

const transitionTargets: Partial<Record<ProposalState, ProposalState[]>> = {
  draft: ['validated'],
  validated: ['submitted'],
  submitted: ['reviewed'],
  reviewed: ['accepted', 'rejected', 'revision'],
  revision: ['draft'],
}

const observationByID = computed(() => {
  const map = new Map<number, SurveyObservation>()
  for (const item of observations.items) map.set(item.id, item)
  return map
})

async function load() {
  await Promise.all([
    parcels.fetch({ page_size: 100 }),
    observations.fetch({ page_size: 100 }),
    proposals.fetch({ page_size: 100 }),
  ])
}

async function create() {
  await proposals.create({ ...form, observation_ids: [...form.observation_ids] })
  createOpen.value = false
  await load()
}

function selectParcel(parcelID: number) {
  form.base_version = parcels.items.find((item) => item.id === parcelID)?.boundary_version ?? 1
  form.observation_ids = []
}

// Backend is authoritative; this mirrors the service gate so reviewers and
// authors see the blocked move before clicking it.
function evidenceBlocks(item: BoundaryProposal, to: ProposalState) {
  if (to !== 'submitted' && to !== 'accepted') return false
  return (item.invalid_evidence ?? []).length > 0
}

function canTransition(item: BoundaryProposal, to: ProposalState) {
  const actorID = auth.user.value?.id
  if (!actorID) return false
  if (evidenceBlocks(item, to)) return false
  const creatorStep = to === 'validated' || to === 'submitted' || (to === 'draft' && item.proposal_state === 'revision')
  if (creatorStep) {
    return auth.hasRole('admin') || (auth.hasRole('surveyor', 'gis_analyst') && item.created_by === actorID)
  }
  return auth.hasRole('admin') || (auth.hasRole('reviewer') && item.created_by !== actorID)
}

function availableTransitions(item: BoundaryProposal) {
  return (transitionTargets[item.proposal_state] ?? []).filter((state) => canTransition(item, state))
}

// Submit/accept still appear in the flow menu as disabled context: the gate is
// enforced server-side, this only communicates why.
function blockedTargets(item: BoundaryProposal) {
  const actorID = auth.user.value?.id
  if (!actorID) return []
  return (transitionTargets[item.proposal_state] ?? []).filter((to) => {
    if (!evidenceBlocks(item, to)) return false
    const creatorStep = to === 'submitted'
    if (creatorStep) {
      return auth.hasRole('admin') || (auth.hasRole('surveyor', 'gis_analyst') && item.created_by === actorID)
    }
    return auth.hasRole('admin') || (auth.hasRole('reviewer') && item.created_by !== actorID)
  })
}

function proposalObservationIDs(item: BoundaryProposal): number[] {
  if (Array.isArray(item.observation_ids)) return item.observation_ids
  try {
    const parsed = JSON.parse(item.observation_ids)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return item.observation_ids.split(',').filter(Boolean).map(Number)
  }
}

function observationCount(item: BoundaryProposal) {
  return proposalObservationIDs(item).length
}

function invalidEvidence(item: BoundaryProposal) {
  return item.invalid_evidence ?? []
}

function invalidTargetLabel(to: ProposalState) {
  return to === 'accepted' ? '采纳已拦截' : '提交已拦截'
}

function canReplaceEvidence(item: BoundaryProposal) {
  const actorID = auth.user.value?.id
  if (!actorID) return false
  if (item.proposal_state !== 'draft' && item.proposal_state !== 'validated' && item.proposal_state !== 'revision') return false
  if (!auth.hasRole('admin') && !(auth.hasRole('surveyor', 'gis_analyst') && item.created_by === actorID)) return false
  return invalidEvidence(item).length > 0
}

function referencedObservationLabel(state: string) {
  return observationStateLabel[state as keyof typeof observationStateLabel] ?? state
}

function observationOptionLabel(observation: SurveyObservation) {
  return `${observation.observation_code} · ${referencedObservationLabel(observation.observation_state)} · ±${observation.horizontal_accuracy_m}m`
}

async function advance(item: BoundaryProposal, to: ProposalState) {
  try {
    await proposals.transition(item.id, { to, version: item.version })
  } finally {
    await load()
  }
}

function showEvidence(item: BoundaryProposal) {
  selected.value = item
  evidenceOpen.value = true
}

function openReplace(item: BoundaryProposal) {
  replacing.value = item
  replacementForm.version = item.version
  replacementForm.observation_ids = proposalObservationIDs(item).filter((id) => {
    const observation = observationByID.value.get(id)
    return observation ? isUsableObservation(observation.observation_state) : false
  })
  replaceOpen.value = true
}

const replacementOptions = computed(() => {
  if (!replacing.value) return []
  return observations.items.filter(
    (item) => item.parcel_id === replacing.value?.parcel_id && isUsableObservation(item.observation_state),
  )
})

async function saveReplacement() {
  if (!replacing.value) return
  try {
    await proposals.replaceEvidence(replacing.value.id, {
      version: replacementForm.version,
      observation_ids: [...replacementForm.observation_ids],
    })
    replaceOpen.value = false
  } finally {
    await load()
  }
}

onMounted(load)
</script>

<template>
  <PageHeader title="边界提案" eyebrow="BOUNDARY PROPOSALS" description="以地块版本为基线，记录观测证据、吸附容差和内部复核状态。">
    <el-button v-if="auth.hasRole('surveyor', 'gis_analyst', 'admin')" type="primary" @click="createOpen = true"><Plus :size="15" />新建提案</el-button>
  </PageHeader>

  <section class="content-band">
    <div class="toolbar"><el-button @click="load"><RefreshCw :size="15" />刷新</el-button><TopologyLegend /><span class="toolbar-spacer subtle-count">{{ proposals.items.length }} 个提案</span></div>
    <div class="data-surface">
      <el-table v-loading="proposals.loading" :data="proposals.items" row-key="id">
        <el-table-column label="提案" width="90"><template #default="scope"><strong>#{{ scope.row.id }}</strong><small class="muted">v{{ scope.row.version }}</small></template></el-table-column>
        <el-table-column label="地块" width="130"><template #default="scope">#{{ scope.row.parcel_id }} · 基线 v{{ scope.row.base_version }}</template></el-table-column>
        <el-table-column label="证据" min-width="210">
          <template #default="scope">
            <div class="evidence-cell">
              <span>{{ observationCount(scope.row) }} 条</span>
              <el-tooltip v-if="invalidEvidence(scope.row).length" placement="bottom" :show-after="150" popper-class="invalid-evidence-tip">
                <template #content>
                  <div class="invalid-tip">
                    <strong>失效观测（{{ invalidEvidence(scope.row).length }}）</strong>
                    <div v-for="entry in invalidEvidence(scope.row)" :key="entry.observation_id" class="invalid-tip-row">
                      <span>{{ entry.observation_code || `#${entry.observation_id}` }}</span>
                      <span class="invalid-tip-reason">{{ entry.reason }}<template v-if="entry.replaced_by_code"> · 替代：{{ entry.replaced_by_code }}</template></span>
                    </div>
                  </div>
                </template>
                <el-tag type="danger" size="small" class="invalid-tag"><ShieldAlert :size="12" />{{ invalidEvidence(scope.row).length }} 条失效</el-tag>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120"><template #default="scope"><ProposalStateBadge :state="scope.row.proposal_state" /></template></el-table-column>
        <el-table-column label="面积变化" width="125"><template #default="scope"><span :class="scope.row.area_delta_square_m >= 0 ? 'positive' : 'negative'">{{ scope.row.area_delta_square_m >= 0 ? '+' : '' }}{{ scope.row.area_delta_square_m.toFixed(2) }} m²</span></template></el-table-column>
        <el-table-column prop="rationale" label="理由" min-width="180" show-overflow-tooltip />
        <el-table-column label="动作" width="230">
          <template #default="scope">
            <el-button text @click="showEvidence(scope.row)">几何</el-button>
            <el-button v-if="canReplaceEvidence(scope.row)" text type="warning" @click="openReplace(scope.row)">更换证据</el-button>
            <el-dropdown v-if="availableTransitions(scope.row).length || blockedTargets(scope.row).length" trigger="click" @command="advance(scope.row, $event)">
              <el-button text type="primary">流转<ChevronDown :size="14" /></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-for="target in availableTransitions(scope.row)" :key="target" :command="target">{{ proposalStateLabel[target] }}</el-dropdown-item>
                  <el-dropdown-item v-for="target in blockedTargets(scope.row)" :key="`blocked-${target}`" :command="target" disabled>
                    <span class="blocked-menu-item"><AlertTriangle :size="13" />{{ proposalStateLabel[target] }}（{{ invalidTargetLabel(target) }}）</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
        <el-table-column v-if="proposals.items.some((item) => invalidEvidence(item).length)" label="失效证据明细" min-width="320">
          <template #default="scope">
            <div v-if="invalidEvidence(scope.row).length" class="invalid-detail">
              <el-alert type="error" :closable="false" show-icon>
                <template #title>
                  <span class="invalid-detail-title">提交与采纳已拦截，作者须更换为同一地块仍有效的观测</span>
                </template>
                <template #default>
                  <ul class="invalid-list">
                    <li v-for="entry in invalidEvidence(scope.row)" :key="entry.observation_id">
                      <strong>{{ entry.observation_code || `#${entry.observation_id}` }}</strong>
                      <span>{{ entry.reason }}</span>
                      <small v-if="entry.state">（{{ referencedObservationLabel(entry.state) }}<template v-if="entry.replaced_by_code">，替代观测：{{ entry.replaced_by_code }}</template><template v-else-if="entry.replaced_by_id">，替代观测：#{{ entry.replaced_by_id }}</template>）</small>
                      <small v-if="entry.quality_note" class="invalid-note">说明：{{ entry.quality_note }}</small>
                    </li>
                  </ul>
                </template>
              </el-alert>
            </div>
            <span v-else class="muted">证据有效</span>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="!proposals.loading && !proposals.items.length" class="empty-state"><div><strong>暂无提案</strong><span>创建提案后，可在冲突消解页运行确定性检测。</span></div></div>
    </div>
  </section>

  <el-dialog v-model="createOpen" title="新建边界提案" width="min(680px, calc(100vw - 28px))">
    <el-form label-position="top">
      <div class="form-grid">
        <el-form-item label="地块"><el-select v-model="form.parcel_id" placeholder="选择地块" style="width: 100%" @change="selectParcel"><el-option v-for="parcel in parcels.items" :key="parcel.id" :label="`${parcel.parcel_code} · v${parcel.boundary_version}`" :value="parcel.id" /></el-select></el-form-item>
        <el-form-item label="基线版本"><el-input-number v-model="form.base_version" :min="1" style="width: 100%" /></el-form-item>
        <el-form-item label="吸附容差（m）"><el-input-number v-model="form.snap_tolerance_m" :min="0.01" :max="1000" :precision="2" style="width: 100%" /></el-form-item>
        <el-form-item label="观测证据（仅可选择同一地块仍有效的观测）"><el-select v-model="form.observation_ids" multiple collapse-tags collapse-tags-tooltip placeholder="选择观测" style="width: 100%"><el-option v-for="observation in observations.items.filter((item) => item.parcel_id === form.parcel_id)" :key="observation.id" :label="observationOptionLabel(observation)" :value="observation.id" :disabled="!isUsableObservation(observation.observation_state)"><span :class="isUsableObservation(observation.observation_state) ? '' : 'option-stale'">{{ observation.observation_code }} · {{ isUsableObservation(observation.observation_state) ? '有效' : '已失效' }}</span></el-option></el-select></el-form-item>
      </div>
      <el-form-item label="提案边界 GeoJSON"><el-input v-model="form.proposed_geojson" type="textarea" :rows="5" /></el-form-item>
      <el-form-item label="理由"><el-input v-model="form.rationale" type="textarea" :rows="3" maxlength="2000" show-word-limit /></el-form-item>
    </el-form>
    <template #footer><el-button @click="createOpen = false">取消</el-button><el-button type="primary" :disabled="!form.parcel_id || !form.rationale" @click="create">保存提案</el-button></template>
  </el-dialog>

  <el-dialog v-model="replaceOpen" title="更换提案证据" width="min(620px, calc(100vw - 28px))">
    <el-alert v-if="replacing" type="warning" :closable="false" show-icon class="replace-alert">
      <template #title>提案 #{{ replacing.id }} 引用的观测有失效记录</template>
      <div class="replace-invalid">
        <div v-for="entry in invalidEvidence(replacing)" :key="entry.observation_id" class="replace-invalid-row">
          <strong>{{ entry.observation_code || `#${entry.observation_id}` }}</strong>
          <span>{{ entry.reason }}<template v-if="entry.replaced_by_code">（替代观测：{{ entry.replaced_by_code }}）</template></span>
        </div>
      </div>
    </el-alert>
    <el-form label-position="top">
      <el-form-item label="新的观测证据（仅限同一地块 #{{ replacing?.parcel_id }} 且仍有效的观测；原编号与替代关系继续留档）">
        <el-select v-model="replacementForm.observation_ids" multiple collapse-tags collapse-tags-tooltip placeholder="选择仍有效的观测" style="width: 100%">
          <el-option v-for="observation in replacementOptions" :key="observation.id" :label="`${observation.observation_code} · ±${observation.horizontal_accuracy_m}m`" :value="observation.id" />
        </el-select>
      </el-form-item>
      <p v-if="!replacementOptions.length" class="muted replace-empty">该地块当前没有仍有效的观测，请先在观测页导入或恢复观测。</p>
    </el-form>
    <template #footer><el-button @click="replaceOpen = false">取消</el-button><el-button type="primary" :disabled="!replacementOptions.length" @click="saveReplacement">保存证据</el-button></template>
  </el-dialog>

  <GeometryEvidenceDrawer v-model="evidenceOpen" title="提案几何" :geometry="selected?.proposed_geojson" :explanation="selected ? `提案 #${selected.id} · 吸附容差 ${selected.snap_tolerance_m} m` : ''" />
</template>

<style scoped>
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 14px; }
.muted { display: block; margin-top: 4px; color: var(--text-muted); font-size: 11px; }
.positive { color: #17604e; }.negative { color: #9c3028; }
.evidence-cell { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.invalid-tag { display: inline-flex; align-items: center; gap: 3px; font-weight: 700; }
.invalid-detail :deep(.el-alert__content) { min-width: 0; }
.invalid-detail-title { font-size: 12px; font-weight: 800; }
.invalid-list { margin: 4px 0 0; padding-left: 16px; display: grid; gap: 2px; font-size: 12px; }
.invalid-list strong { margin-right: 6px; }
.invalid-note { display: block; color: var(--text-muted); }
.blocked-menu-item { display: inline-flex; align-items: center; gap: 5px; color: var(--danger); }
.option-stale { color: var(--text-muted); }
.replace-alert { margin-bottom: 12px; }
.replace-invalid { display: grid; gap: 4px; margin-top: 4px; font-size: 12px; }
.replace-empty { margin: 0; }
@media (max-width: 620px) { .form-grid { grid-template-columns: 1fr; } }
</style>

<style>
.invalid-evidence-tip { max-width: 360px; }
.invalid-tip { display: grid; gap: 6px; }
.invalid-tip-row { display: grid; gap: 1px; }
.invalid-tip-reason { color: #f3c7c2; font-size: 12px; }
</style>
