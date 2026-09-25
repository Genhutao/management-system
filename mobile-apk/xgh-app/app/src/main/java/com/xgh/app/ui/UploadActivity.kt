package com.xgh.app.ui

import android.os.Bundle
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.lifecycle.lifecycleScope
import coil.load
import com.xgh.app.R
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.UploadPhotoResponse
import com.xgh.app.databinding.ActivityUploadBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.RequestBody.Companion.asRequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.File

/**
 * 三态上报：实拍（photo）/ 记名纸条（note）/ 纯文本（text）。
 * report_kind != "text" 时后端必要求 image（无图直接 400）。
 */
class UploadActivity : BaseActivity() {

    private lateinit var binding: ActivityUploadBinding
    private var kind: String = "photo"

    /**
     * 上报类别固定用后端可识别的规范值：时段计数按 photo_type 精确匹配，
     * 自由文本会导致"本时段已上报 N 条"永远为 0。
     */
    private val photoTypeOptions = listOf(
        "violation" to R.string.photo_type_violation,
        "sanitation" to R.string.photo_type_sanitation,
        "duty" to R.string.photo_type_duty,
    )
    private var photoType: String = "violation"

    private val takePicture =
        registerForActivityResult(ActivityResultContracts.TakePicture()) { ok ->
            if (ok) onPhotoTaken()
        }

    private var photoFile: File? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityUploadBinding.inflate(layoutInflater)
        setContentView(binding.root)

        val photoTypeLabels = photoTypeOptions.map { getString(it.second) }
        val photoTypeValues = photoTypeOptions.map { it.first }
        binding.actPhotoType.setSimpleItems(photoTypeLabels.toTypedArray())
        binding.actPhotoType.setText(photoTypeLabels[0], false)
        binding.actPhotoType.setOnItemClickListener { _, _, pos, _ ->
            photoType = photoTypeValues.getOrElse(pos) { "violation" }
        }

        binding.tgKind.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            kind = when (checkedId) {
                R.id.btnKindNote -> "note"
                R.id.btnKindText -> "text"
                else -> "photo"
            }
            binding.photoBox.visibility = if (kind != "text") View.VISIBLE else View.GONE
            binding.tilSubjects.visibility = if (kind == "photo") View.GONE else View.VISIBLE
        }
        binding.tgKind.check(R.id.btnKindPhoto)

        binding.photoBox.setOnClickListener {
            val dir = File(cacheDir, "camera").apply { mkdirs() }
            val file = File(dir, "report_${System.currentTimeMillis()}.jpg")
            val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", file)
            pendingFile = file
            takePicture.launch(uri)
        }

        binding.toolbar.setNavigationOnClickListener { finish() }
        binding.btnSubmit.setOnClickListener { submit() }
    }

    private var pendingFile: File? = null

    private fun onPhotoTaken() {
        photoFile = pendingFile
        binding.ivPhoto.load(photoFile) { crossfade(true) }
        binding.tvPhotoHint.visibility = View.GONE
    }

    private fun submit() {
        val room = binding.etRoom.text?.toString()?.trim().orEmpty()
        if (room.isEmpty()) {
            Ui.toast(this, getString(R.string.err_need_room))
            return
        }
        if (kind != "text" && (photoFile == null || !photoFile!!.exists())) {
            Ui.toast(this, getString(R.string.err_need_image))
            return
        }

        binding.progress.visibility = View.VISIBLE
        binding.btnSubmit.isEnabled = false

        val app = application as XghApp
        lifecycleScope.launch {
            try {
                val building = app.sessionStore.userBuilding.first() ?: ""
                val part = photoFile?.takeIf { kind != "text" }?.let { f ->
                    MultipartBody.Part.createFormData(
                        "image", f.name, f.asRequestBody("image/jpeg".toMediaType())
                    )
                }
                val resp = ApiClient.get().service.uploadPhoto(
                    room.toPlain(),
                    photoType.toPlain(),
                    building.toPlain(),
                    kind.toPlain(),
                    binding.etNote.text?.toString()?.trim().orEmpty().toPlain(),
                    binding.etSubjects.text?.toString()?.trim().orEmpty().toPlain(),
                    part
                ) as UploadPhotoResponse
                renderResult(resp)
                Ui.toast(this@UploadActivity, getString(R.string.submit_ok))
                // 留 1 秒展示 AI 徽标与名单命中数，然后自动返回主页
                delay(1000)
                finish()
            } catch (e: retrofit2.HttpException) {
                val body = try { e.response()?.errorBody()?.string() } catch (_: Exception) { null }
                val msg = Ui.extractError(body)
                Ui.toast(this@UploadActivity, msg ?: getString(R.string.net_error))
            } catch (e: Exception) {
                Ui.toast(this@UploadActivity, getString(R.string.net_error))
            } finally {
                binding.progress.visibility = View.GONE
                binding.btnSubmit.isEnabled = true
            }
        }
    }

    private fun renderResult(resp: UploadPhotoResponse) {
        val text = when (resp.ai_status) {
            "real" -> getString(R.string.ai_real)
            "failed" -> getString(R.string.ai_failed)
            else -> getString(R.string.ai_disabled)
        }
        binding.tvAiStatus.visibility = View.VISIBLE
        binding.tvAiStatus.text = buildString {
            append(text)
            val total = resp.subject_total ?: 0
            if (total > 0) {
                append("\n")
                append(getString(R.string.subjects_matched, resp.subject_matched ?: 0, total))
            }
        }
    }

    private fun String.toPlain() = this.toRequestBody("text/plain".toMediaType())
}
