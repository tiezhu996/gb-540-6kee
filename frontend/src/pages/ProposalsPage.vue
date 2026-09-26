<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { AlertTriangle, ChevronDown, Plus, RefreshCw, Wrench } from 'lucide-vue-next'
import PageHeader from '@/components/common/PageHeader.vue'
import ProposalStateBadge from '@/components/common/ProposalStateBadge.vue'
import GeometryEvidenceDrawer from '@/components/common/GeometryEvidenceDrawer.vue'
import TopologyLegend from '@/components/common/TopologyLegend.vue'
import { useBoundaryProposalStore } from '@/stores/boundary-proposal'
import { useLandParcelStore } from '@/stores/land-parcel'
import { useSurveyObservationStore } from '@/stores/survey-observation'
import { useAuth } from '@/hooks/useAuth'
import { proposalStateLabel, type ProposalState } from '@/types/enums/proposal-state'
import type { BoundaryProposal } from '@/types/boundary-proposal'
import type { SurveyObservation } from '@/types/survey-observation'

const proposals = useBoundaryProposalStore()
const parcels = useLandParcelStore()
const observations = useSurveyObservationStore()
const auth = useAuth()
const createOpen = ref(false)
const evidenceOpen = ref(false)
const repairOpen = ref(false)
const selected = ref<BoundaryProposal | null>(null)
const repairing = ref<BoundaryProposal | null>(null)
const form = reactive({
  parcel_id: 0,
  base_version: 1,
  proposed_geojson: '{"type":"Polygon","coordinates":[[[0,0],[100,0],[100,100],[0,100],[0,0]]]}',
  observation_ids: [] as number[],
  snap_tolerance_m: 0.5,
  rationale: '',
})
const repairForm = reactive({ observation_ids: [] as number[] })

const transitionTargets: Partial<Record<ProposalState, ProposalState[]>> = {
  draft: ['validated'],
  validated: ['submitted'],
  submitted: ['reviewed'],
  reviewed: ['accepted', 'rejected', 'revision'],
  revision: ['draft'],
}

const invalidObservationLabel: Record<string, string> = {
  rejected: '复核拒绝',
  superseded: '已被替代',
  missing: '记录缺失',
  foreign_parcel: '属于其他地块',
}

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

function proposalObservationIDs(item: BoundaryProposal): number[] {
  if (Array.isArray(item.observation_ids)) return item.observation_ids
  try {
    const parsed = JSON.parse(item.observation_ids)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return item.observation_ids.split(',').map(Number).filter(Boolean)
  }
}

function canTransition(item: BoundaryProposal, to: ProposalState) {
  const actorID = auth.user.value?.id
  if (!actorID) return false
  const creatorStep = to === 'validated' || to === 'submitted' || (to === 'draft' && item.proposal_state === 'revision')
  if (creatorStep) {
    return auth.hasRole('admin') || (auth.hasRole('surveyor', 'gis_analyst') && item.created_by === actorID)
  }
  return auth.hasRole('admin') || (auth.hasRole('reviewer') && item.created_by !== actorID)
}

function availableTransitions(item: BoundaryProposal) {
  return (transitionTargets[item.proposal_state] ?? []).filter((state) => {
    // Submission and acceptance must not be offered while evidence is void;
    // the backend rejects them anyway, but the UI hides the dead end.
    if ((state === 'submitted' || state === 'accepted') && hasInvalidEvidence(item)) return false
    return canTransition(item, state)
  })
}

function hasInvalidEvidence(item: BoundaryProposal) {
  return !!item.evidence && !item.evidence.evidence_valid
}

function invalidEvidence(item: BoundaryProposal) {
  return item.evidence?.invalid_observations ?? []
}

function observationCount(item: BoundaryProposal) {
  return proposalObservationIDs(item).length
}

const canAuthor = computed(() => auth.hasRole('surveyor', 'gis_analyst', 'admin'))

function canRepair(item: BoundaryProposal) {
  const actorID = auth.user.value?.id
  if (!actorID || !hasInvalidEvidence(item)) return false
  if (item.proposal_state !== 'draft' && item.proposal_state !== 'validated' && item.proposal_state !== 'revision') return false
  return auth.hasRole('admin') || (canAuthor.value && item.created_by === actorID)
}

function repairOptions(item: BoundaryProposal): SurveyObservation[] {
  return observations.items.filter(
    (obs) => obs.parcel_id === item.parcel_id && obs.observation_state === 'accepted',
  )
}

function openRepair(item: BoundaryProposal) {
  repairing.value = item
  // Keep still-valid references preselected; voided ids start unchecked.
  const invalidIDs = new Set(invalidEvidence(item).map((entry) => entry.observation_id))
  repairForm.observation_ids = proposalObservationIDs(item).filter((id) => !invalidIDs.has(id))
  repairOpen.value = true
}

async function submitRepair() {
  if (!repairing.value) return
  await proposals.updateEvidence(repairing.value.id, {
    version: repairing.value.version,
    observation_ids: [...repairForm.observation_ids],
  })
  repairOpen.value = false
  await load()
}

async function advance(item: BoundaryProposal, to: ProposalState) {
  await proposals.transition(item.id, { to, version: item.version })
  await proposals.fetch({ page_size: 100 })
}

function showEvidence(item: BoundaryProposal) {
  selected.value = item
  evidenceOpen.value = true
}

onMounted(load)
</script>

<template>
  <PageHeader title="边界提案" eyebrow="BOUNDARY PROPOSALS" description="以地块版本为基线，记录观测证据、吸附容差和内部复核状态。">
    <el-button v-if="canAuthor" type="primary" @click="createOpen = true"><Plus :size="15" />新建提案</el-button>
  </PageHeader>

  <section class="content-band">
    <div class="toolbar"><el-button @click="load"><RefreshCw :size="15" />刷新</el-button><TopologyLegend /><span class="toolbar-spacer subtle-count">{{ proposals.items.length }} 个提案</span></div>
    <div class="data-surface">
      <el-table v-loading="proposals.loading" :data="proposals.items" row-key="id">
        <el-table-column label="提案" width="90"><template #default="scope"><strong>#{{ scope.row.id }}</strong><small class="muted">v{{ scope.row.version }}</small></template></el-table-column>
        <el-table-column label="地块" width="130"><template #default="scope">#{{ scope.row.parcel_id }} · 基线 v{{ scope.row.base_version }}</template></el-table-column>
        <el-table-column label="证据" width="110">
          <template #default="scope">
            <span>{{ observationCount(scope.row) }} 条</span>
            <el-tooltip v-if="hasInvalidEvidence(scope.row)" placement="top" :show-after="150">
              <template #content>
                <div v-for="entry in invalidEvidence(scope.row)" :key="entry.observation_id" class="invalid-tip">
                  <strong>{{ entry.observation_code }}</strong>（{{ invalidObservationLabel[entry.state] ?? entry.state }}）：{{ entry.reason }}
                </div>
              </template>
              <span class="invalid-count"><AlertTriangle :size="13" />{{ invalidEvidence(scope.row).length }} 条失效</span>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120"><template #default="scope"><ProposalStateBadge :state="scope.row.proposal_state" /></template></el-table-column>
        <el-table-column label="面积变化" width="125"><template #default="scope"><span :class="scope.row.area_delta_square_m >= 0 ? 'positive' : 'negative'">{{ scope.row.area_delta_square_m >= 0 ? '+' : '' }}{{ scope.row.area_delta_square_m.toFixed(2) }} m²</span></template></el-table-column>
        <el-table-column prop="rationale" label="理由" min-width="200" show-overflow-tooltip />
        <el-table-column label="动作" width="220">
          <template #default="scope">
            <el-button text @click="showEvidence(scope.row)">几何</el-button>
            <el-button v-if="canRepair(scope.row)" text type="warning" @click="openRepair(scope.row)"><Wrench :size="14" />更换证据</el-button>
            <el-dropdown v-if="availableTransitions(scope.row).length" trigger="click" @command="advance(scope.row, $event)">
              <el-button text type="primary">流转<ChevronDown :size="14" /></el-button>
              <template #dropdown><el-dropdown-menu><el-dropdown-item v-for="target in availableTransitions(scope.row)" :key="target" :command="target">{{ proposalStateLabel[target] }}</el-dropdown-item></el-dropdown-menu></template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
      <el-alert
        v-for="item in proposals.items.filter(hasInvalidEvidence)"
        :key="`invalid-${item.id}`"
        class="invalid-banner"
        type="error"
        :closable="false"
        show-icon
      >
        <template #title>提案 #{{ item.id }} 的证据已失效，提交与内部采纳已被阻断</template>
        <div class="invalid-detail">
          <div v-for="entry in invalidEvidence(item)" :key="entry.observation_id" class="invalid-entry">
            <strong>{{ entry.observation_code }}</strong>
            <el-tag size="small" type="danger" effect="plain">{{ invalidObservationLabel[entry.state] ?? entry.state }}</el-tag>
            <span>{{ entry.reason }}</span>
            <span v-if="entry.replaced_by_id" class="muted">原编号留档，替代关系：→ {{ entry.replaced_by_code ? `${entry.replaced_by_code} ` : '' }}#{{ entry.replaced_by_id }}</span>
          </div>
          <div class="invalid-hint">
            请由作者改用同一地块仍为「accepted」的观测支撑提案后，才能继续提交与复核流程。
            <el-button v-if="canRepair(item)" text type="primary" @click="openRepair(item)"><Wrench :size="13" />立即更换证据</el-button>
            <span v-else-if="item.proposal_state === 'submitted' || item.proposal_state === 'reviewed'" class="muted">需由复核员退回「待修订」后作者才能更换证据。</span>
          </div>
        </div>
      </el-alert>
      <div v-if="!proposals.loading && !proposals.items.length" class="empty-state"><div><strong>暂无提案</strong><span>创建提案后，可在冲突消解页运行确定性检测。</span></div></div>
    </div>
  </section>

  <el-dialog v-model="createOpen" title="新建边界提案" width="min(680px, calc(100vw - 28px))">
    <el-form label-position="top">
      <div class="form-grid">
        <el-form-item label="地块"><el-select v-model="form.parcel_id" placeholder="选择地块" style="width: 100%" @change="selectParcel"><el-option v-for="parcel in parcels.items" :key="parcel.id" :label="`${parcel.parcel_code} · v${parcel.boundary_version}`" :value="parcel.id" /></el-select></el-form-item>
        <el-form-item label="基线版本"><el-input-number v-model="form.base_version" :min="1" style="width: 100%" /></el-form-item>
        <el-form-item label="吸附容差（m）"><el-input-number v-model="form.snap_tolerance_m" :min="0.01" :max="1000" :precision="2" style="width: 100%" /></el-form-item>
        <el-form-item label="观测证据（仅同地块 accepted）"><el-select v-model="form.observation_ids" multiple collapse-tags collapse-tags-tooltip placeholder="选择观测" style="width: 100%"><el-option v-for="observation in observations.items.filter((item) => item.parcel_id === form.parcel_id && item.observation_state === 'accepted')" :key="observation.id" :label="`${observation.observation_code} · ±${observation.horizontal_accuracy_m}m`" :value="observation.id" /><template #empty><span class="muted">该地块没有可用于支撑提案的有效观测</span></template></el-select></el-form-item>
      </div>
      <el-form-item label="提案边界 GeoJSON"><el-input v-model="form.proposed_geojson" type="textarea" :rows="5" /></el-form-item>
      <el-form-item label="理由"><el-input v-model="form.rationale" type="textarea" :rows="3" maxlength="2000" show-word-limit /></el-form-item>
    </el-form>
    <template #footer><el-button @click="createOpen = false">取消</el-button><el-button type="primary" :disabled="!form.parcel_id || !form.rationale" @click="create">保存提案</el-button></template>
  </el-dialog>

  <el-dialog v-model="repairOpen" title="更换失效证据" width="min(560px, calc(100vw - 28px))">
    <div v-if="repairing" class="repair-body">
      <el-alert type="warning" :closable="false" show-icon>
        <template #title>提案 #{{ repairing.id }} 当前引用的以下观测已失效（原编号继续留档）：</template>
        <div v-for="entry in invalidEvidence(repairing)" :key="entry.observation_id" class="invalid-entry">
          <strong>{{ entry.observation_code }}</strong>
          <el-tag size="small" type="danger" effect="plain">{{ invalidObservationLabel[entry.state] ?? entry.state }}</el-tag>
          <span>{{ entry.reason }}</span>
        </div>
      </el-alert>
      <el-form label-position="top" class="repair-form">
        <el-form-item label="改用同一地块仍有效的观测">
          <el-select v-model="repairForm.observation_ids" multiple collapse-tags collapse-tags-tooltip placeholder="选择 accepted 观测" style="width: 100%">
            <el-option v-for="observation in repairOptions(repairing)" :key="observation.id" :label="`${observation.observation_code} · ±${observation.horizontal_accuracy_m}m`" :value="observation.id" />
            <template #empty><span class="muted">该地块没有 accepted 观测，无法继续流转</span></template>
          </el-select>
        </el-form-item>
      </el-form>
      <p class="muted repair-note">更换后提案回到「草拟」并重新计版本，需再次校验、提交和复核；挑错地块或已作废的观测会被后端拒绝，提案保持原状态。</p>
    </div>
    <template #footer><el-button @click="repairOpen = false">取消</el-button><el-button type="primary" @click="submitRepair">保存并回到草拟</el-button></template>
  </el-dialog>

  <GeometryEvidenceDrawer v-model="evidenceOpen" title="提案几何" :geometry="selected?.proposed_geojson" :explanation="selected ? `提案 #${selected.id} · 吸附容差 ${selected.snap_tolerance_m} m` : ''" />
</template>

<style scoped>
.form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 14px; }
.muted { display: block; margin-top: 4px; color: var(--text-muted); font-size: 11px; }
.positive { color: #17604e; }.negative { color: #9c3028; }
.invalid-count { display: inline-flex; align-items: center; gap: 3px; margin-left: 6px; padding: 1px 6px; border-radius: 3px; color: #9c3028; background: #fbeceb; border: 1px solid #e6afaa; font-size: 11px; font-weight: 700; cursor: help; }
.invalid-tip { max-width: 320px; margin: 2px 0; }
.invalid-banner { margin: 12px; }
.invalid-detail { display: flex; flex-direction: column; gap: 6px; margin-top: 4px; }
.invalid-entry { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; font-size: 13px; }
.invalid-entry .muted { display: inline; margin-top: 0; }
.invalid-hint { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 4px; font-size: 13px; }
.invalid-hint .muted { display: inline; margin-top: 0; }
.repair-body { display: flex; flex-direction: column; gap: 14px; }
.repair-note { margin: 0; }
@media (max-width: 620px) { .form-grid { grid-template-columns: 1fr; } }
</style>
