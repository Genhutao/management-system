package com.xgh.app.ui

import android.content.Intent
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.RecyclerView
import androidx.recyclerview.widget.StaggeredGridLayoutManager
import com.google.android.material.chip.Chip
import com.xgh.app.R
import com.xgh.app.data.ApiClient
import com.xgh.app.data.InspectionPhoto
import com.xgh.app.data.InspectionsResponse
import com.xgh.app.databinding.ActivityHistoryBinding
import com.xgh.app.databinding.ItemInspectionBinding
import com.xgh.app.util.Ui
import com.xgh.app.XghApp
import coil.load
import kotlinx.coroutines.launch

/** 历史上报瀑布流：GET /dorm/inspections（硬编码 Limit 50，按 building 隔离） */
class HistoryActivity : AppCompatActivity() {

    private lateinit var binding: ActivityHistoryBinding
    private val adapter = HistoryAdapter()
    private var severity: String? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityHistoryBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.rvHistory.layoutManager =
            StaggeredGridLayoutManager(2, StaggeredGridLayoutManager.VERTICAL)
        binding.rvHistory.adapter = adapter
        binding.refresh.setOnRefreshListener { refresh() }
        binding.btnBack.setOnClickListener { finish() }

        setupSeverityChips()
        refresh()
    }

    private fun setupSeverityChips() {
        val options = listOf(
            "" to getString(R.string.filter_all),
            "high" to getString(R.string.filter_severity_high),
            "medium" to getString(R.string.filter_severity_medium),
            "low" to getString(R.string.filter_severity_low)
        )
        options.forEachIndexed { index, (value, label) ->
            val chip = Chip(this)
            chip.text = label
            chip.isCheckable = true
            chip.id = View.generateViewId()
            chip.setOnClickListener {
                severity = value.ifEmpty { null }
                refresh()
            }
            binding.chipSeverity.addView(chip)
            if (index == 0) chip.isChecked = true
        }
    }

    private fun refresh() {
        lifecycleScope.launch {
            binding.refresh.isRefreshing = true
            val resp = Ui.request(this@HistoryActivity) {
                ApiClient.get().service.inspections(null, severity)
            } as InspectionsResponse?
            binding.refresh.isRefreshing = false
            binding.progress.visibility = View.GONE
            val items = resp?.items.orEmpty()
            binding.tvEmpty.visibility = if (items.isEmpty()) View.VISIBLE else View.GONE
            adapter.submit(items)
        }
    }

    private inner class HistoryAdapter : RecyclerView.Adapter<HistoryHolder>() {
        private val items = mutableListOf<InspectionPhoto>()
        fun submit(list: List<InspectionPhoto>) {
            items.clear(); items.addAll(list); notifyDataSetChanged()
        }
        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): HistoryHolder =
            HistoryHolder(ItemInspectionBinding.inflate(LayoutInflater.from(parent.context), parent, false))
        override fun getItemCount() = items.size
        override fun onBindViewHolder(holder: HistoryHolder, position: Int) =
            holder.bind(items[position])
    }

    private inner class HistoryHolder(val b: ItemInspectionBinding) :
        RecyclerView.ViewHolder(b.root) {

        fun bind(item: InspectionPhoto) {
            b.tvRoom.text = "${item.building ?: ""} ${item.room_number ?: ""}"
            val (label, colorRes) = when (item.severity) {
                "high" -> "严重" to R.color.error
                "medium" -> "中等" to R.color.warning
                "low" -> "轻微" to R.color.success
                else -> "待核对" to R.color.info_neutral
            }
            b.tvSeverity.text = label
            b.tvSeverity.background.mutate().setTint(
                androidx.core.content.ContextCompat.getColor(b.root.context, colorRes)
            )
            b.tvMeta.text = listOfNotNull(
                item.manager_name?.let { "宿管：$it" },
                item.created_at?.take(16)?.replace('T', ' ')
            ).joinToString(" · ")
            val note = item.note_text?.takeIf { it.isNotBlank() }
            b.tvNote.visibility = if (note != null) View.VISIBLE else View.GONE
            b.tvNote.text = note

            val app = application as XghApp
            lifecycleScope.launch {
                val url = app.sessionStore.imageUrl(item.image_url)
                if (url != null) b.ivPhoto.load(url) { crossfade(true) }
            }
        }
    }
}
