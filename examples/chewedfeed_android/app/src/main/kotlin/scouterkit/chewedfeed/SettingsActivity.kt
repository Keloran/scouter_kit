package scouterkit.chewedfeed

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import java.util.Locale

// App settings. For now this phone's display settings for the glasses; setup, calibration and ownership
// controls come with CF-45.
class SettingsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.settings)
        val current = DisplaySettings.load(this)
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(32, 32, 32, 32) }
        fun field(label: Int, value: String, decimal: Boolean = false) = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER or if (decimal) InputType.TYPE_NUMBER_FLAG_DECIMAL else InputType.TYPE_NUMBER_FLAG_SIGNED
            setText(value) // ASCII digits: parsed back with toIntOrNull / toDoubleOrNull
        }.also {
            list.addView(TextView(this).apply { setText(label) })
            list.addView(it)
        }
        list.addView(TextView(this).apply { setText(R.string.display_settings) })
        val dx = field(R.string.dx_label, String.format(Locale.ROOT, "%d", current.dx))
        val dy = field(R.string.dy_label, String.format(Locale.ROOT, "%d", current.dy))
        val scale = field(R.string.scale_label, String.format(Locale.ROOT, "%.2f", current.scale).trimEnd('0').trimEnd('.'), decimal = true)
        val state = TextView(this)
        list.addView(Button(this).apply {
            setText(R.string.save)
            setOnClickListener {
                val s = DisplaySettings(dx.text.toString().toIntOrNull() ?: Int.MIN_VALUE,
                    dy.text.toString().toIntOrNull() ?: Int.MIN_VALUE, scale.text.toString().replace(',', '.').toDoubleOrNull() ?: Double.NaN)
                val problem = s.problem()
                if (problem != null) {
                    state.text = problem
                } else {
                    s.save(this@SettingsActivity)
                    state.setText(R.string.saved)
                }
            }
        })
        list.addView(state)
        setContentView(ScrollView(this).apply { addView(list) })
    }
}
